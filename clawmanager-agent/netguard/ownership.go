package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type linkRecord struct {
	Name  string `json:"ifname"`
	Alias string `json:"ifalias"`
	Info  struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}
type tableRecord struct{ Family, Name string }

func (h *Handle) links(ctx context.Context) ([]linkRecord, error) {
	data, err := h.run(ctx, "", ipTool, "-j", "-d", "link", "show")
	if err != nil {
		return nil, err
	}
	return decodeLinks(data)
}

func decodeLinks(data []byte) ([]linkRecord, error) {
	var links []linkRecord
	if json.Unmarshal(data, &links) != nil || links == nil {
		return nil, fmt.Errorf("netguard: invalid link ownership inventory")
	}
	for _, link := range links {
		if link.Name == "" {
			return nil, fmt.Errorf("netguard: incomplete link ownership inventory")
		}
	}
	return links, nil
}

func (h *Handle) verifyContainedLinks(ctx context.Context) error {
	data, err := h.run(ctx, "", nsenterTool, "--no-fork", "--net="+h.NamespacePath(), "--", ipTool, "-j", "-d", "link", "show")
	if err != nil {
		return err
	}
	links, err := decodeLinks(data)
	if err != nil {
		return err
	}
	for _, name := range []string{h.host, h.peer} {
		found := false
		for _, link := range links {
			if link.Name == name {
				if link.Alias != h.ownerMarker || link.Info.Kind != "veth" {
					return h.ownershipError("contained veth", name)
				}
				found = true
			}
		}
		if !found {
			return h.ownershipError("contained veth", name)
		}
	}
	return nil
}

func (h *Handle) ownerChain() string {
	return "owner_" + strings.TrimPrefix(h.ownerMarker, "cm-netguard:")
}

func (h *Handle) tableHasOwnerChain(ctx context.Context) (bool, error) {
	data, err := h.run(ctx, "", nftTool, "-j", "list", "table", "inet", h.table)
	if err != nil {
		return false, err
	}
	var inventory struct {
		NFTables *[]map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(data, &inventory) != nil || inventory.NFTables == nil {
		return false, fmt.Errorf("netguard: invalid full-table ownership inventory")
	}
	tableCount, ownerCount := 0, 0
	for _, entry := range *inventory.NFTables {
		if len(entry) != 1 {
			return false, fmt.Errorf("netguard: malformed full-table ownership inventory")
		}
		for kind, raw := range entry {
			switch kind {
			case "metainfo":
				continue
			case "table":
				var table tableRecord
				if json.Unmarshal(raw, &table) != nil || table.Family != "inet" || table.Name != h.table {
					return false, fmt.Errorf("netguard: mismatched ownership table")
				}
				tableCount++
			case "chain", "rule":
				var object struct {
					Family, Table, Name, Chain string
					Hook                       *string
				}
				if json.Unmarshal(raw, &object) != nil || object.Family != "inet" || object.Table != h.table || kind == "chain" && object.Name == "" || kind == "rule" && object.Chain == "" {
					return false, fmt.Errorf("netguard: malformed ownership %s", kind)
				}
				if kind == "chain" && object.Name == h.ownerChain() {
					if object.Hook != nil {
						return false, fmt.Errorf("netguard: owner chain must not have a hook")
					}
					ownerCount++
				}
			default:
				return false, fmt.Errorf("netguard: unexpected ownership object %q", kind)
			}
		}
	}
	if tableCount != 1 || ownerCount > 1 {
		return false, fmt.Errorf("netguard: incomplete or duplicate ownership inventory")
	}
	return ownerCount == 1, nil
}

func (h *Handle) tables(ctx context.Context) ([]tableRecord, error) {
	data, err := h.run(ctx, "", nftTool, "-j", "list", "tables")
	if err != nil {
		return nil, err
	}
	var inventory struct {
		NFTables *[]map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(data, &inventory) != nil || inventory.NFTables == nil {
		return nil, fmt.Errorf("netguard: invalid table ownership inventory")
	}
	tables := []tableRecord{}
	for _, entry := range *inventory.NFTables {
		if raw, ok := entry["table"]; ok {
			var table tableRecord
			if json.Unmarshal(raw, &table) != nil || table.Name == "" || table.Family == "" {
				return nil, fmt.Errorf("netguard: incomplete table ownership inventory")
			}
			tables = append(tables, table)
		} else if _, ok := entry["metainfo"]; !ok {
			return nil, fmt.Errorf("netguard: unexpected table ownership inventory")
		}
	}
	return tables, nil
}

func (h *Handle) checkAvailable(ctx context.Context) error {
	if _, err := h.statNamespace(); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("netguard: namespace name is occupied")
		}
		return err
	}
	links, err := h.links(ctx)
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Name == h.host || link.Name == h.peer {
			return fmt.Errorf("netguard: veth name is occupied")
		}
	}
	tables, err := h.tables(ctx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Family == "inet" && table.Name == h.table {
			return fmt.Errorf("netguard: table name is occupied")
		}
	}
	return nil
}

func (h *Handle) ownershipError(kind, name string) error {
	return fmt.Errorf("netguard: uncertain ownership of %s %s; refusing deletion; residual evidence: %s", kind, name, h.statePath)
}

func (h *Handle) cleanupLink(ctx context.Context) error {
	if !h.linkAttempted && !h.linkOwned {
		return nil
	}
	links, err := h.links(ctx)
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Name != h.host {
			continue
		}
		if link.Alias != h.ownerMarker || link.Info.Kind != "veth" {
			h.linkOwned = false
			h.linkAttempted = true
			return h.ownershipError("veth", h.host)
		}
		if err = h.execute(ctx, "", ipTool, "link", "delete", h.host); err != nil {
			// A failed delete can also have committed. Only absence resolves it.
			after, queryErr := h.links(ctx)
			if queryErr != nil {
				return errors.Join(err, queryErr)
			}
			for _, present := range after {
				if present.Name == h.host {
					return err
				}
			}
		}
		break
	}
	h.linkAttempted = false
	h.linkOwned = false
	return nil
}

func (h *Handle) cleanupTable(ctx context.Context) error {
	if !h.tableAttempted && !h.tableOwned {
		return nil
	}
	tables, err := h.tables(ctx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Family != "inet" || table.Name != h.table {
			continue
		}
		owned, ownershipErr := h.tableHasOwnerChain(ctx)
		if ownershipErr != nil {
			return ownershipErr
		}
		if !owned {
			h.tableOwned = false
			h.tableAttempted = true
			return h.ownershipError("table", h.table)
		}
		if err = h.execute(ctx, "", nftTool, "delete", "table", "inet", h.table); err != nil {
			after, queryErr := h.tables(ctx)
			if queryErr != nil {
				return errors.Join(err, queryErr)
			}
			for _, present := range after {
				if present.Family == "inet" && present.Name == h.table {
					return err
				}
			}
		}
		break
	}
	h.tableAttempted = false
	h.tableOwned = false
	return nil
}

func (h *Handle) cleanupNamespace(ctx context.Context) error {
	if !h.namespaceAttempted && !h.namespaceOwned {
		return nil
	}
	info, err := h.statNamespace()
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		// A name alone cannot attribute an uncertain ip-netns result to us.
		if h.namespaceInfo == nil || !os.SameFile(h.namespaceInfo, info) {
			h.namespaceOwned = false
			h.namespaceAttempted = true
			return h.ownershipError("namespace", h.name)
		}
		if err = h.execute(ctx, "", ipTool, "netns", "delete", h.name); err != nil {
			if _, statErr := h.statNamespace(); !errors.Is(statErr, os.ErrNotExist) {
				return errors.Join(err, statErr)
			}
		}
	}
	h.namespaceAttempted = false
	h.namespaceOwned = false
	return nil
}

// Persist intent before invoking a creation tool. Unknown outcomes survive as
// explicit evidence even though Prepare must return no usable handle on error.
func (h *Handle) persistOwnership() error {
	status := func(attempted, owned bool) string {
		if owned {
			return "owned"
		}
		if attempted {
			return "uncertain"
		}
		return "absent"
	}
	var receipt any
	if h.namespaceInfo != nil {
		receipt = fileReceipt(h.namespaceInfo)
	}
	data, err := json.MarshalIndent(map[string]any{
		"owner_marker": h.ownerMarker,
		"owner_chain":  h.ownerChain(),
		"namespace":    h.name, "namespace_status": status(h.namespaceAttempted, h.namespaceOwned), "namespace_receipt": receipt,
		"host_link": h.host, "peer_link": h.peer, "link_status": status(h.linkAttempted, h.linkOwned),
		"table": h.table, "table_status": status(h.tableAttempted, h.tableOwned),
	}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(h.statePath, ".ownership-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, writeErr := f.Write(append(data, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(h.statePath, "ownership.json"))
}

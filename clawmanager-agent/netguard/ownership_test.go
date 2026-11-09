package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The kernel can commit a command's side effect before the command's exit is
// observed. This fake keeps those effects independently of its returned errors.
type kernelLink struct {
	PeerName string `json:"-"`
	Name     string `json:"ifname"`
	Alias    string `json:"ifalias"`
	Info     struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}
type kernelTable struct {
	Family, Name, Comment string
	OwnerChain            string
	Handle                uint64
}
type kernelState struct {
	links                map[string]kernelLink
	childLinks           map[string]kernelLink
	tables               map[string]kernelTable
	afterCreate          string
	raceForeign          string
	deleteNames          []string
	namespaceFile        string
	ignoreCreateMetadata bool
}

func (k *kernelState) run(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
	return k.execute(ctx, input, tool, args, false)
}

func (k *kernelState) execute(ctx context.Context, input, tool string, args []string, inChild bool) ([]byte, error) {
	if k.links == nil {
		k.links = map[string]kernelLink{}
	}
	if k.childLinks == nil {
		k.childLinks = map[string]kernelLink{}
	}
	if k.tables == nil {
		k.tables = map[string]kernelTable{}
	}
	if tool == nsenterTool {
		for i, arg := range args {
			if arg == "--" && i+1 < len(args) {
				return k.execute(ctx, input, args[i+1], args[i+2:], true)
			}
		}
		return nil, errors.New("invalid namespace command")
	}
	links := k.links
	if inChild {
		links = k.childLinks
	}
	marshal := func(v any) ([]byte, error) { return json.Marshal(v) }
	if tool == ipTool && len(args) > 0 && args[0] == "-j" {
		records := []kernelLink{}
		for _, link := range links {
			records = append(records, link)
		}
		return marshal(records)
	}
	if tool == nftTool && len(args) > 0 && args[0] == "-j" {
		entries := []any{}
		full := len(args) > 4 && args[2] == "table"
		for _, table := range k.tables {
			if full && table.Name != args[4] {
				continue
			}
			record := map[string]any{"family": table.Family, "name": table.Name, "handle": table.Handle}
			if !k.ignoreCreateMetadata {
				record["comment"] = table.Comment
			}
			entries = append(entries, map[string]any{"table": record})
			if full && table.OwnerChain != "" {
				entries = append(entries, map[string]any{"chain": map[string]any{"family": table.Family, "table": table.Name, "name": table.OwnerChain}})
			}
		}
		return marshal(map[string]any{"nftables": entries})
	}
	if tool == ipTool && len(args) > 1 && args[0] == "netns" {
		if args[1] == "add" && k.namespaceFile != "" {
			if err := os.WriteFile(k.namespaceFile, nil, 0600); err != nil {
				return nil, err
			}
			if k.afterCreate == "namespace" {
				return nil, context.Canceled
			}
			return nil, nil
		}
		if args[1] == "delete" && k.namespaceFile != "" {
			k.deleteNames = append(k.deleteNames, "namespace")
			for name := range k.childLinks {
				for parentName, parent := range k.links {
					if parent.PeerName == name {
						delete(k.links, parentName)
					}
				}
			}
			clear(k.childLinks)
			return nil, os.Remove(k.namespaceFile)
		}
	}
	if tool == ipTool && len(args) > 2 && args[0] == "link" && args[1] == "add" {
		name := args[2]
		if !inChild && k.raceForeign == "link" {
			k.links[name] = kernelLink{Name: name, Alias: "foreign"}
		}
		if _, exists := links[name]; exists {
			return nil, errors.New("EEXIST: foreign link")
		}
		link := kernelLink{Name: name}
		link.Info.Kind = "veth"
		for i, arg := range args {
			if arg == "alias" && i+1 < len(args) {
				link.Alias = args[i+1]
				break
			}
		}
		if k.ignoreCreateMetadata {
			link.Alias = ""
		}
		for i, arg := range args {
			if arg == "peer" && i+2 < len(args) {
				peer := kernelLink{Name: args[i+2], PeerName: name}
				peer.Info.Kind = "veth"
				link.PeerName = peer.Name
				links[peer.Name] = peer
				break
			}
		}
		links[name] = link
		if k.afterCreate == "link" {
			return nil, context.Canceled
		}
	}
	if tool == ipTool && len(args) > 4 && args[0] == "link" && args[1] == "set" && args[3] == "alias" {
		link, ok := links[args[2]]
		if !ok {
			return nil, errors.New("alias target absent")
		}
		link.Alias = args[4]
		links[args[2]] = link
		if k.afterCreate == "alias" {
			return nil, context.Canceled
		}
	}
	if inChild && tool == ipTool && len(args) > 4 && args[0] == "link" && args[1] == "set" && args[3] == "netns" {
		name := args[2]
		if k.raceForeign == "link" {
			k.links[name] = kernelLink{Name: name, Alias: "foreign"}
		}
		if _, exists := k.links[name]; exists {
			return nil, errors.New("EEXIST: foreign parent link")
		}
		link, ok := k.childLinks[name]
		if !ok {
			return nil, errors.New("move target absent")
		}
		k.links[name] = link
		delete(k.childLinks, name)
		if k.afterCreate == "move" {
			return nil, context.Canceled
		}
	}
	if tool == nftTool && input != "" {
		if inChild {
			return nil, nil
		}
		fields := strings.Fields(input)
		name, owner := "", ""
		for i, arg := range fields {
			if arg == "table" && i+2 < len(fields) && name == "" {
				name = fields[i+2]
			}
			if arg == "chain" && i+1 < len(fields) && strings.HasPrefix(fields[i+1], "owner_") {
				owner = fields[i+1]
			}
		}
		if k.raceForeign == "table" {
			k.tables[name] = kernelTable{Family: "inet", Name: name, Comment: "foreign", Handle: 99}
		}
		if _, exists := k.tables[name]; exists && strings.HasPrefix(input, "create table") {
			return nil, errors.New("EEXIST: foreign table")
		}
		comment := ""
		if _, rest, ok := strings.Cut(input, "comment \""); ok {
			comment, _, _ = strings.Cut(rest, "\"")
		}
		if k.ignoreCreateMetadata {
			comment = ""
		}
		k.tables[name] = kernelTable{Family: "inet", Name: name, Comment: comment, OwnerChain: owner, Handle: 42}
		if k.afterCreate == "table" {
			return nil, context.Canceled
		}
	}
	if tool == ipTool && len(args) > 2 && args[0] == "link" && args[1] == "delete" {
		k.deleteNames = append(k.deleteNames, args[2])
		link := links[args[2]]
		delete(links, args[2])
		delete(k.childLinks, link.PeerName)
	}
	if tool == nftTool && len(args) > 3 && args[0] == "delete" {
		k.deleteNames = append(k.deleteNames, args[3])
		delete(k.tables, args[3])
	}
	return nil, nil
}

func TestObservedKernelIgnoresCreationAliasesAndTableComments(t *testing.T) {
	h := testHandle(t, &recorder{})
	k := &kernelState{ignoreCreateMetadata: true, namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
	h.run = func(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
		if tool == ipTool && len(args) > 1 && args[0] == "link" && args[1] == "add" {
			t.Fatal("untagged veth created in parent")
		}
		joined := strings.Join(args, " ")
		if tool == nsenterTool && strings.Contains(joined, "link set "+h.host+" netns ") {
			if !strings.HasSuffix(joined, "netns "+strconv.Itoa(os.Getpid())) {
				t.Fatal("namespace move did not target trusted supervisor PID", joined)
			}
			if k.childLinks[h.host].Alias != h.ownerMarker || k.childLinks[h.peer].Alias != h.ownerMarker {
				t.Fatal("veth exported before both explicit aliases persisted")
			}
		}
		return k.run(ctx, input, tool, args...)
	}
	if err := h.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("observed creation metadata behavior breaks cleanup: %v", err)
	}
	if len(k.links) != 0 || len(k.tables) != 0 {
		t.Fatal("kernel objects leaked")
	}
}

func TestUntaggedCreationRemainsContainedWhenNamespaceCleanupFails(t *testing.T) {
	h := testHandle(t, &recorder{})
	k := &kernelState{afterCreate: "link", ignoreCreateMetadata: true, namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
	cleanupFailure := errors.New("namespace cleanup unavailable")
	h.run = func(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
		if tool == ipTool && len(args) > 1 && args[0] == "netns" && args[1] == "delete" {
			return nil, cleanupFailure
		}
		return k.run(ctx, input, tool, args...)
	}
	err := h.prepare(context.Background())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupFailure) {
		t.Fatal("lost uncertainty/cleanup error", err)
	}
	if len(k.links) != 0 || len(k.childLinks) != 2 {
		t.Fatalf("untagged creation escaped owned namespace: parent=%v child=%v", k.links, k.childLinks)
	}
	for _, link := range k.childLinks {
		if link.Alias != "" {
			t.Fatal("fake did not reproduce ignored creation aliases")
		}
	}
	data, err := os.ReadFile(filepath.Join(h.statePath, "ownership.json"))
	if err != nil || !strings.Contains(string(data), `"namespace_status": "owned"`) {
		t.Fatalf("missing containment evidence: %s %v", data, err)
	}
	h.run = k.run
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(k.childLinks) != 0 {
		t.Fatal("contained retry leaked pair")
	}
}

func TestTableCleanupRejectsMalformedOrForeignOwnerChainInventory(t *testing.T) {
	for _, broken := range []string{"wrong-family", "wrong-table", "missing-owner", "hooked-owner", "malformed"} {
		t.Run(broken, func(t *testing.T) {
			h := testHandle(t, &recorder{})
			k := &kernelState{ignoreCreateMetadata: true, namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
			h.run = k.run
			if err := h.prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			k.deleteNames = nil
			h.run = func(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
				if tool == nftTool && len(args) > 4 && args[0] == "-j" && args[2] == "table" {
					if broken == "malformed" {
						return []byte(`{"nftables":[{}]}`), nil
					}
					family, table, name := "inet", h.table, h.ownerChain()
					if broken == "wrong-family" {
						family = "ip"
					}
					if broken == "wrong-table" {
						table = "foreign"
					}
					if broken == "missing-owner" {
						name = "owner_foreign"
					}
					chain := map[string]any{"family": family, "table": table, "name": name}
					if broken == "hooked-owner" {
						chain["hook"] = "input"
					}
					return json.Marshal(map[string]any{"nftables": []any{map[string]any{"table": map[string]any{"family": "inet", "name": h.table}}, map[string]any{"chain": chain}}})
				}
				return k.run(ctx, input, tool, args...)
			}
			if err := h.Close(context.Background()); err == nil {
				t.Fatal("accepted invalid ownership inventory")
			}
			if len(k.tables) != 1 {
				t.Fatal("deleted table using invalid ownership inventory")
			}
			for _, name := range k.deleteNames {
				if name == h.table {
					t.Fatal("attempted unsafe table deletion")
				}
			}
			if _, err := os.Stat(filepath.Join(h.statePath, "ownership.json")); err != nil {
				t.Fatal("lost ownership evidence", err)
			}
		})
	}
}

func TestCreationErrorAfterKernelCommitCleansOwnedObjects(t *testing.T) {
	for _, stage := range []string{"link", "alias", "move", "table"} {
		t.Run(stage, func(t *testing.T) {
			h := testHandle(t, &recorder{})
			k := &kernelState{afterCreate: stage, namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
			h.run = k.run
			err := h.prepare(context.Background())
			if !errors.Is(err, context.Canceled) || h.ready {
				t.Fatalf("error=%v ready=%v", err, h.ready)
			}
			if len(k.links) != 0 || len(k.childLinks) != 0 || len(k.tables) != 0 {
				t.Fatalf("committed object leaked: links=%v tables=%v", k.links, k.tables)
			}
		})
	}
}

func TestForeignResourcesAreNeverAdoptedOrDeleted(t *testing.T) {
	for _, kind := range []string{"link", "table"} {
		for _, racing := range []bool{false, true} {
			t.Run(kind+"/racing="+map[bool]string{false: "false", true: "true"}[racing], func(t *testing.T) {
				h := testHandle(t, &recorder{})
				k := &kernelState{links: map[string]kernelLink{}, tables: map[string]kernelTable{}, namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
				h.run = k.run
				if racing {
					k.raceForeign = kind
				} else if kind == "link" {
					k.links[h.host] = kernelLink{Name: h.host, Alias: "foreign"}
				} else {
					k.tables[h.table] = kernelTable{Family: "inet", Name: h.table, Comment: "foreign", Handle: 99}
				}
				if err := h.prepare(context.Background()); err == nil || h.ready {
					t.Fatalf("foreign %s adopted, error=%v ready=%v", kind, err, h.ready)
				}
				if kind == "link" && k.links[h.host].Alias != "foreign" {
					t.Fatal("foreign link changed/deleted")
				}
				if kind == "table" && k.tables[h.table].Comment != "foreign" {
					t.Fatal("foreign table changed/deleted")
				}
				for _, name := range k.deleteNames {
					if kind == "link" && name == h.host || kind == "table" && name == h.table {
						t.Fatal("attempted foreign resource deletion", name)
					}
				}
				if racing {
					if data, err := os.ReadFile(filepath.Join(h.statePath, "ownership.json")); err != nil || !strings.Contains(string(data), "uncertain") {
						t.Fatalf("missing uncertain ownership evidence: %s %v", data, err)
					}
				}
			})
		}
	}
}

func TestUncertainNamespaceCreationRetainsEvidenceWithoutDeletingByName(t *testing.T) {
	h := testHandle(t, &recorder{})
	k := &kernelState{afterCreate: "namespace", namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
	h.run = k.run
	err := h.prepare(context.Background())
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "uncertain ownership") {
		t.Fatalf("lost uncertain outcome: %v", err)
	}
	if len(k.deleteNames) != 0 {
		t.Fatal("deleted namespace based only on its name", k.deleteNames)
	}
	if _, err := os.Stat(k.namespaceFile); err != nil {
		t.Fatal("uncertain namespace lost", err)
	}
	data, err := os.ReadFile(filepath.Join(h.statePath, "ownership.json"))
	if err != nil || !strings.Contains(string(data), `"namespace_status": "uncertain"`) {
		t.Fatalf("missing namespace evidence: %s %v", data, err)
	}
}

func TestCleanupPreservesReplacementsWithSameResourceNames(t *testing.T) {
	for _, kind := range []string{"link", "table", "namespace"} {
		t.Run(kind, func(t *testing.T) {
			h := testHandle(t, &recorder{})
			k := &kernelState{namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
			h.run = k.run
			if err := h.prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			k.deleteNames = nil
			switch kind {
			case "link":
				k.links[h.host] = kernelLink{Name: h.host, Alias: "foreign"}
			case "table":
				k.tables[h.table] = kernelTable{Family: "inet", Name: h.table, Comment: "foreign", Handle: 100}
			case "namespace":
				if err := os.Rename(k.namespaceFile, k.namespaceFile+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(k.namespaceFile, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "uncertain ownership") {
				t.Fatalf("replacement accepted: %v", err)
			}
			for _, name := range k.deleteNames {
				if kind == "link" && name == h.host || kind == "table" && name == h.table || kind == "namespace" && name == "namespace" {
					t.Fatal("deleted replacement", name)
				}
			}
			data, err := os.ReadFile(filepath.Join(h.statePath, "ownership.json"))
			if err != nil || !strings.Contains(string(data), `"`+kind+`_status": "uncertain"`) {
				t.Fatalf("missing replacement evidence: %s %v", data, err)
			}
		})
	}
}

func TestPreexistingPeerAndNamespacePreventAnyCreation(t *testing.T) {
	for _, kind := range []string{"peer", "namespace"} {
		t.Run(kind, func(t *testing.T) {
			r := &recorder{}
			h := testHandle(t, r)
			if kind == "peer" {
				r.kernel.links = map[string]kernelLink{h.peer: {Name: h.peer, Alias: "foreign"}}
			} else if err := os.WriteFile(r.kernel.namespaceFile, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := h.prepare(context.Background()); err == nil {
				t.Fatal("accepted occupied name")
			}
			for _, call := range r.calls {
				joined := strings.Join(call.args, " ")
				if strings.Contains(joined, "netns add") || strings.Contains(joined, "link add") || strings.Contains(joined, "delete") || call.input != "" {
					t.Fatalf("mutated network on preexisting %s: %+v", kind, call)
				}
			}
		})
	}
}

func TestUnverifiableCreationRetainsEvidenceAndCanRetry(t *testing.T) {
	h := testHandle(t, &recorder{})
	k := &kernelState{afterCreate: "move", namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
	committed := false
	inventoryFailure := errors.New("ownership inventory unavailable")
	h.run = func(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
		if committed && tool == ipTool && len(args) > 0 && args[0] == "-j" {
			return nil, inventoryFailure
		}
		data, err := k.run(ctx, input, tool, args...)
		if tool == nsenterTool && strings.Contains(strings.Join(args, " "), "link set "+h.host+" netns ") {
			committed = true
		}
		return data, err
	}
	err := h.prepare(context.Background())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, inventoryFailure) {
		t.Fatalf("lost setup/ownership errors: %v", err)
	}
	if len(k.links) != 0 || len(k.childLinks) != 0 || len(k.deleteNames) != 1 || k.deleteNames[0] != "namespace" {
		t.Fatalf("unsafe cleanup despite unavailable ownership: %v %v", k.links, k.deleteNames)
	}
	if data, err := os.ReadFile(filepath.Join(h.statePath, "ownership.json")); err != nil || !strings.Contains(string(data), `"link_status": "uncertain"`) {
		t.Fatalf("missing unresolved evidence: %s %v", data, err)
	}
	h.run = k.run
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(k.links) != 0 {
		t.Fatal("retry leaked confirmed owned veth")
	}
	if _, err := os.Stat(h.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retry retained resolved state", err)
	}
}

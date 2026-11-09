module github.com/iamlovingit/clawmanager-legacy-agent

go 1.26

require (
	clawmanager-hermes-image v0.0.0
	github.com/iamlovingit/clawmanager-agent v0.0.0
	github.com/iamlovingit/clawmanager-deepseek-harness-image v0.0.0
	github.com/iamlovingit/clawmanager-openclaw-image v0.0.0
	github.com/iamlovingit/clawmanager-opencode-image v0.0.0
)

require (
	golang.org/x/sys v0.35.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/iamlovingit/clawmanager-agent => ../clawmanager-agent

replace github.com/iamlovingit/clawmanager-openclaw-image => ../openclaw

replace clawmanager-hermes-image => ../hermes

replace github.com/iamlovingit/clawmanager-opencode-image => ../opencode

replace github.com/iamlovingit/clawmanager-deepseek-harness-image => ../deepseek-harness

package main

import (
	lite "clawmanager-hermes-image/lite"
	"github.com/iamlovingit/clawmanager-agent/app"
)

var version = "dev"

func main() { app.Version = version; app.Run(lite.NewProfile("hermes")) }

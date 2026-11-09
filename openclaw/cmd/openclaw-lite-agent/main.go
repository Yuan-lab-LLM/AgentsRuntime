package main

import (
	"github.com/iamlovingit/clawmanager-agent/app"
	lite "github.com/iamlovingit/clawmanager-openclaw-image/lite"
)

var version = "dev"

func main() { app.Version = version; app.Run(lite.NewProfile("openclaw")) }

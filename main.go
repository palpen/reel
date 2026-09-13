package main

import (
	"github.com/pspenano/reel/cmd"
	"os"
)

var version = "dev"

func main() { os.Exit(cmd.Run(os.Args[1:], version)) }

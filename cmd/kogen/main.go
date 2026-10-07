package main

import (
	"kogen-go/internal/app"
	"os"
)

func main() { os.Exit(app.Bootstrap("kogen", os.Stderr)) }

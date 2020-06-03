package main

import "fmt"

type VersionCommand struct{}

var versionCommand VersionCommand

func (v *VersionCommand) Execute(args []string) error {
	fmt.Println(Version)
	return nil
}

func init() {
	parser.AddCommand("version", "Print version", "Print version", &versionCommand)
}

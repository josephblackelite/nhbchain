package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	commands := [][]string{
		{"buf", "format", "-w"},
		{"buf", "lint"},
	}

	if against := os.Getenv("BUF_BREAKING_AGAINST"); against != "" {
		commands = append(commands, []string{"buf", "breaking", "--against", against})
	}

	commands = append(commands, []string{"buf", "generate"})

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "proto generation step failed: %v\n", err)
			os.Exit(1)
		}
	}
}

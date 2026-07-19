package main

import (
	"bufio"
	"os"
)

func readCommands() (<-chan string, error) {
	cfg := activeConfig()
	f, err := os.Open(cfg.CommandsFilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	commandsChan := make(chan string)
	spawn("readCommands", func() {
		for scanner.Scan() {
			command := scanner.Text()
			commandsChan <- command
		}
		close(commandsChan)
	})
	return commandsChan, nil
}

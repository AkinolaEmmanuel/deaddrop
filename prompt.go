package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

type SessionInput struct {
	Action     PromptAction
	RoomID     string
	Passphrase string
	FilePath   string // source file to send; only set for PromptCreate
}

type PromptAction uint8

const (
	PromptJoin PromptAction = iota
	PromptCreate
)

func (a *PromptAction) String() string {
	if *a == PromptCreate {
		return "create"
	}
	return "join"
}

func RunPrompt() (*SessionInput, error) {
	reader := bufio.NewReader(os.Stdin)

	action, err := promptAction(reader)
	if err != nil {
		return nil, err
	}

	roomID, err := promptRoomID(reader, action)
	if err != nil {
		return nil, err
	}

	// Prompt for the source file before the passphrase. The passphrase read
	// bypasses the bufio reader (term.ReadPassword reads the fd directly), so
	// it must come last to avoid discarding buffered stdin.
	var filePath string
	if action == PromptCreate {
		filePath, err = promptFilePath(reader)
		if err != nil {
			return nil, err
		}
	}

	passphrase, err := promptPassphrase(reader)
	if err != nil {
		return nil, err
	}

	return &SessionInput{
		Action:     action,
		RoomID:     roomID,
		Passphrase: passphrase,
		FilePath:   filePath,
	}, nil
}

func promptFilePath(reader *bufio.Reader) (string, error) {
	for {
		fmt.Print("Enter the path of the file to send: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read file path: %w", err)
		}

		input = strings.TrimSpace(input)

		if input == "" {
			fmt.Println("File path cannot be empty. Please try again.")
			continue
		}

		if _, err := os.Stat(input); err != nil {
			fmt.Printf("Cannot access file %q: %v\n", input, err)
			continue
		}

		return input, nil
	}
}

func promptAction(reader *bufio.Reader) (PromptAction, error) {
	for {
		fmt.Println()
		fmt.Println("Welcome to DeadDrop!")
		fmt.Println("-------------------")
		fmt.Println("This tool allows you to safely deaddrop secret files with a peer using end-to-end encryption.")
		fmt.Println("Do you want to join an existing room or create a new one?")
		fmt.Println("1. Join an existing room")
		fmt.Println("2. Create a new room")
		fmt.Println("Enter your choice (1 or 2): ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return PromptJoin, err
		}

		input = strings.TrimSpace(input)
		switch input {
		case "1":
			return PromptJoin, nil
		case "2":
			return PromptCreate, nil
		default:
			fmt.Println("Invalid choice. Please enter 1 or 2.")
		}
	}
}

func promptRoomID(reader *bufio.Reader, action PromptAction) (string, error) {
	var label string
	switch action {
	case PromptJoin:
		label = "Enter the Room ID shared with you: "
	case PromptCreate:
		label = "Enter a room ID to share with your peers: "
	}

	for {
		fmt.Print(label)

		input, err := reader.ReadString('\n')

		if err != nil {
			return "", fmt.Errorf("failed to read room ID: %w", err)
		}

		input = strings.TrimSpace(input)

		if input == "" {
			fmt.Println("Room ID cannot be empty. Please try again.")
			continue
		}

		return input, nil
	}

}

func promptPassphrase(reader *bufio.Reader) (string, error) {
	fmt.Print("Enter the passphrase for the room: ")

	fd := int(os.Stdin.Fd())

	if term.IsTerminal(fd) {
		passBytes, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("failed to read passphrase: %w", err)
		}
		passphrase := strings.TrimSpace(string(passBytes))
		if passphrase == "" {
			return "", errors.New("passphrase cannot be empty")
		}
		return passphrase, nil

	}

	fmt.Fprintln(os.Stderr, "Warning: Passphrase input is not a terminal. Your input will be visible.")
	// Reuse the caller's reader: a fresh bufio.Reader would discard any stdin
	// already buffered by earlier prompts, causing a spurious EOF on piped input.
	passphrase, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read passphrase: %w", err)
	}

	passphrase = strings.TrimSpace(passphrase)
	if passphrase == "" {
		return "", errors.New("passphrase cannot be empty")
	}
	return passphrase, nil
}

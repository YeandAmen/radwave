package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// loadEnvironment reads an explicitly supplied Vercel env-pull file without
// printing its values or expanding shell commands.
func loadEnvironment(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("invalid environment entry on line %d", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("invalid quoted environment value on line %d", line)
			}
			value = decoded
		} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
			value = strings.Trim(value, "'")
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("invalid environment key on line %d", line)
		}
	}
	return scanner.Err()
}

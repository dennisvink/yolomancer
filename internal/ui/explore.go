package ui

import (
	"encoding/json"
	"strings"

	"github.com/dennisvink/yolomancer/internal/model"
)

type exploringOperation struct {
	kind  string
	value string
}

func exploringOperations(call model.ToolCall) []exploringOperation {
	path := stringArgument(call.Arguments, "path")
	switch call.Name {
	case "read_file":
		return []exploringOperation{{"Read", fallback(path, ".")}}
	case "list_files", "repo_snapshot":
		return []exploringOperation{{"List", fallback(path, ".")}}
	case "shell", "exec_command":
		command := stringArgument(call.Arguments, "cmd")
		if command == "" {
			command = stringArgument(call.Arguments, "command")
		}
		return shellExploringOperations(command)
	default:
		return nil
	}
}

func shellExploringOperations(command string) []exploringOperation {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", "|", "\n", ";", "\n")
	var operations []exploringOperation
	for _, raw := range strings.Split(replacer.Replace(command), "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
			fields = fields[1:]
		}
		if len(fields) == 0 || fields[0] == "cd" || fields[0] == "pwd" {
			continue
		}
		commandName := fields[0]
		if index := strings.LastIndex(commandName, "/"); index >= 0 {
			commandName = commandName[index+1:]
		}
		switch commandName {
		case "cat", "head", "tail", "less", "more", "nl", "wc", "xxd", "sed", "awk":
			if commandName == "sed" && hasInPlaceFlag(fields) {
				return nil
			}
			fallbackValue := unquotedCommand(fields)
			if commandName == "sed" && len(fields) <= 3 || commandName == "awk" && len(fields) <= 2 {
				operations = append(operations, exploringOperation{"Read", truncatePlain(fallbackValue, 160)})
			} else {
				operations = append(operations, exploringOperation{"Read", shellTarget(fields, fallbackValue)})
			}
		case "ls", "tree", "find", "fd":
			operations = append(operations, exploringOperation{"List", shellTarget(fields, ".")})
		case "grep", "rg", "ag":
			operations = append(operations, exploringOperation{"Search", searchSummary(fields)})
		case "git":
			if len(fields) > 1 && fields[1] == "grep" {
				operations = append(operations, exploringOperation{"Search", searchSummary(fields[1:])})
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return operations
}

func hasInPlaceFlag(fields []string) bool {
	for _, field := range fields {
		if field == "-i" || strings.HasPrefix(field, "-i") {
			return true
		}
	}
	return false
}

func shellTarget(fields []string, fallbackValue string) string {
	for index := len(fields) - 1; index > 0; index-- {
		value := strings.Trim(fields[index], "'\"")
		if !strings.HasPrefix(value, "-") && value != "" && !numericRange(value) {
			return truncatePlain(value, 160)
		}
	}
	return fallbackValue
}

func numericRange(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && r != ',' {
			return false
		}
	}
	return value != ""
}

func unquotedCommand(fields []string) string {
	cleaned := make([]string, len(fields))
	for index, field := range fields {
		cleaned[index] = strings.Trim(field, "'\"")
	}
	return strings.Join(cleaned, " ")
}

func searchSummary(fields []string) string {
	var positional []string
	for _, field := range fields[1:] {
		if !strings.HasPrefix(field, "-") {
			positional = append(positional, strings.Trim(field, "'\""))
		}
	}
	if len(positional) > 1 {
		return truncatePlain(positional[0], 80) + " in " + truncatePlain(positional[len(positional)-1], 160)
	}
	if len(positional) == 1 {
		return truncatePlain(positional[0], 160)
	}
	return truncatePlain(strings.Join(fields, " "), 160)
}

func stringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}

func fallback(value, fallbackValue string) string {
	if value == "" {
		return fallbackValue
	}
	return value
}

func exploringDisplay(operations []exploringOperation, active bool) string {
	header := "• Explored"
	if active {
		header = "• Exploring"
	}
	lines := []string{header}
	for index := 0; index < len(operations); {
		operation := operations[index]
		if operation.kind == "Read" {
			var values []string
			for index < len(operations) && operations[index].kind == "Read" {
				value := operations[index].value
				if !contains(values, value) {
					values = append(values, value)
				}
				index++
			}
			lines = append(lines, "  └ Read "+strings.Join(values, ", "))
			continue
		}
		lines = append(lines, "  └ "+operation.kind+" "+operation.value)
		index++
	}
	return strings.Join(lines, "\n")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func successfulToolSummary(output string) bool {
	if strings.TrimSpace(output) == "ok" {
		return true
	}
	var value map[string]any
	return json.Unmarshal([]byte(output), &value) == nil && value["ok"] == true
}

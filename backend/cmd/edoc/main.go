package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/api"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServer()
			return
		case "-p":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "Usage: edoc -p \"prompt\"")
				os.Exit(1)
			}
			runOnce(strings.Join(os.Args[2:], " "))
			return
		case "--help", "-h":
			printUsage()
			return
		}
	}

	runREPL()
}

func printUsage() {
	fmt.Println("edoc-edualc - AI coding assistant")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  edoc                  Start interactive REPL")
	fmt.Println("  edoc -p \"prompt\"      Run a single prompt")
	fmt.Println("  edoc serve            Start the web API server")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  ANTHROPIC_API_KEY     Anthropic API key")
	fmt.Println("  OPENAI_API_KEY        OpenAI API key")
	fmt.Println("  ANTHROPIC_BASE_URL    Custom Anthropic base URL")
	fmt.Println("  OPENAI_BASE_URL       Custom OpenAI base URL")
	fmt.Println("  EDOC_MODEL            Model to use (default: claude-sonnet-4-20250514)")
	fmt.Println("  EDOC_PROVIDER         Provider: anthropic or openai (default: anthropic)")
	fmt.Println("  EDOC_PORT             Server port (default: 8080)")
}

func buildConfig() agent.Config {
	workDir, _ := os.Getwd()
	reg := tool.DefaultRegistry(workDir)
	p := buildProvider()
	model := os.Getenv("EDOC_MODEL")
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	return agent.Config{
		Provider:     p,
		Registry:     reg,
		SystemPrompt: prompt.BuildSystemPrompt(workDir),
		Model:        model,
		MaxTokens:    8192,
	}
}

func buildProvider() provider.Provider {
	providerName := os.Getenv("EDOC_PROVIDER")
	if providerName == "" {
		providerName = "anthropic"
	}

	switch providerName {
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "OPENAI_API_KEY not set")
			os.Exit(1)
		}
		model := os.Getenv("EDOC_MODEL")
		if model == "" {
			model = "gpt-4o"
		}
		return provider.NewOpenAIProvider(apiKey, model, os.Getenv("OPENAI_BASE_URL"))
	default:
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
			os.Exit(1)
		}
		model := os.Getenv("EDOC_MODEL")
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
		return provider.NewAnthropicProvider(apiKey, model, os.Getenv("ANTHROPIC_BASE_URL"))
	}
}

// runOnce executes a single prompt and exits. Maps to `claude -p "..."`.
func runOnce(userPrompt string) {
	cfg := buildConfig()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	for evt := range agent.Run(ctx, cfg, userPrompt) {
		switch evt.Type {
		case "text_delta":
			fmt.Print(evt.Delta)
		case "tool_use":
			fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", evt.ToolName)
		case "tool_result":
			if evt.ToolResult != nil && evt.ToolResult.IsError {
				fmt.Fprintf(os.Stderr, "[tool error: %s]\n", evt.ToolResult.Content)
			}
		case "error":
			fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
			os.Exit(1)
		case "turn_complete":
			fmt.Println()
		}
	}
}

// runREPL starts an interactive read-eval-print loop.
func runREPL() {
	cfg := buildConfig()
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("edoc-edualc (type /quit to exit)")
	fmt.Println()

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "/quit" || input == "/exit" {
			break
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

		for evt := range agent.Run(ctx, cfg, input) {
			switch evt.Type {
			case "text_delta":
				fmt.Print(evt.Delta)
			case "tool_use":
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", evt.ToolName)
			case "tool_result":
				if evt.ToolResult != nil && evt.ToolResult.IsError {
					fmt.Fprintf(os.Stderr, "[tool error: %s]\n", evt.ToolResult.Content)
				}
			case "error":
				fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
			case "turn_complete":
				fmt.Println()
			}
		}

		cancel()
		fmt.Println()
	}
}

// runServer starts the Gin HTTP server.
func runServer() {
	port := os.Getenv("EDOC_PORT")
	if port == "" {
		port = "8080"
	}

	p := buildProvider()
	workDir, _ := os.Getwd()

	r := api.NewRouter(p, workDir)
	fmt.Printf("Starting server on :%s\n", port)
	if err := r.Run(":" + port); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

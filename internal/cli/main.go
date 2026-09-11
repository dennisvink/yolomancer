package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/gitremote"
	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/session"
	"github.com/dennisvink/yolomancer/internal/ui"
	"github.com/google/uuid"
)

type options struct {
	debug, local, noAlt, alt bool
	baseURL                  string
	profile                  string
	command                  string
	helpTarget               string
	args                     []string
}

var inputReader = bufio.NewReader(os.Stdin)

func Main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "git" {
		return gitremote.Run(ctx, args[1:])
	}
	o, err := parse(args)
	if err != nil {
		return err
	}
	debug := o.debug || envDebug()
	base := o.baseURL
	if o.local {
		base = model.LocalBaseURL
	}
	switch o.command {
	case "help":
		if o.helpTarget != "" && !isCommand(o.helpTarget) {
			return fmt.Errorf("unrecognized subcommand `%s`", o.helpTarget)
		}
		commandUsage(os.Stdout, o.helpTarget)
		return nil
	case "version":
		fmt.Println("yolomancer " + model.Version)
		return nil
	case "internal-aws-request":
		return internalAWSRequest(ctx)
	case "logout":
		if len(o.args) != 0 {
			return fmt.Errorf("unexpected argument `%s`", o.args[0])
		}
		removed, err := appconfig.Remove()
		if err != nil {
			return err
		}
		f, _ := appconfig.File()
		if removed {
			fmt.Println("Logged out. Removed " + f)
		} else {
			fmt.Println("No stored config found at " + f)
		}
		return nil
	case "login":
		return login(ctx, o, base, debug)
	case "register":
		return register(ctx, o)
	case "run":
		if len(o.args) != 1 {
			return errors.New("usage: yolomancer run <prompt>")
		}
		cfg, err := loadConfig(ctx, o)
		if err != nil {
			return err
		}
		if base != "" {
			cfg.BaseURL = &base
		}
		a := app.New(cfg, debug)
		sink := stdoutSink{debug: debug}
		_, err = a.RunTurn(ctx, o.args[0], sink)
		return err
	case "resume":
		return resume(ctx, o, base, debug)
	case "":
		cfg, err := loadConfig(ctx, o)
		if err != nil {
			return err
		}
		if base != "" {
			cfg.BaseURL = &base
		}
		return ui.Run(app.New(cfg, debug), nil, o.alt && !o.noAlt)
	default:
		return fmt.Errorf("unknown command `%s`", o.command)
	}
}

func parse(args []string) (options, error) {
	o := options{}
	var rest []string
	commandSeen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--profile":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return o, errors.New("--profile requires a value")
			}
			i++
			o.profile = strings.TrimSpace(args[i])
		case "--debug":
			o.debug = true
		case "--local":
			o.local = true
		case "--no-alt-screen":
			o.noAlt = true
		case "--alt-screen":
			o.alt = true
		case "--version", "-V":
			if commandSeen {
				return o, fmt.Errorf("unexpected argument `%s`", a)
			}
			o.command = "version"
		case "--help", "-h":
			o.command = "help"
			if commandSeen {
				o.helpTarget = rest[0]
			}
		case "--base-url":
			if i+1 >= len(args) {
				return o, errors.New("--base-url requires a value")
			}
			i++
			o.baseURL = strings.TrimRight(args[i], "/")
		default:
			allowedSubcommandFlag := commandSeen && (rest[0] == "login" || rest[0] == "register" && a == "--endpoint" || rest[0] == "resume" && a == "--all")
			if strings.HasPrefix(a, "-") && !allowedSubcommandFlag {
				return o, fmt.Errorf("unexpected argument `%s`", a)
			}
			rest = append(rest, a)
			if len(rest) == 1 && isCommand(a) {
				commandSeen = true
			}
		}
	}
	if o.command != "" {
		return o, nil
	}
	if len(rest) > 0 && isCommand(rest[0]) {
		o.command = rest[0]
		o.args = rest[1:]
		if o.command == "help" && len(o.args) > 0 {
			o.helpTarget = o.args[0]
		}
	} else {
		if len(rest) > 0 {
			return o, fmt.Errorf("unrecognized subcommand `%s`", rest[0])
		}
		o.args = rest
	}
	return o, nil
}
func isCommand(v string) bool {
	return v == "register" || v == "login" || v == "logout" || v == "run" || v == "resume" || v == "help" || v == "internal-aws-request"
}

func loadConfig(ctx context.Context, o options) (*model.Config, error) {
	var cfg *model.Config
	var err error
	if o.profile != "" {
		cfg, err = appconfig.LoadForProfile(o.profile)
	} else {
		cfg, err = appconfig.LoadOrBootstrap()
	}
	if err != nil {
		return nil, err
	}
	if cfg.Registration != nil || cfg.AWSProfile != nil && strings.TrimSpace(*cfg.AWSProfile) != "" {
		if err := provider.PrepareBedrock(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func internalAWSRequest(ctx context.Context) error {
	var payload struct {
		Service, Method, URL, Body, Region string
		Headers                            map[string]string
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8*1024*1024)).Decode(&payload); err != nil {
		return fmt.Errorf("parse internal AWS request: %w", err)
	}
	cfg := &model.Config{}
	if payload.Region != "" {
		cfg.AWSRegion = &payload.Region
	}
	result, err := provider.SignedAWSRequest(ctx, cfg, payload.Service, payload.Method, payload.URL, payload.Body, payload.Headers, payload.Region, nil)
	if err != nil {
		return err
	}
	result["permission_scope"] = "unknown"
	result["aws_operation"] = "aws:SignedRequest"
	result["aws_service"] = "aws"
	return json.NewEncoder(os.Stdout).Encode(result)
}

func login(ctx context.Context, o options, base string, debug bool) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	api := fs.String("api-key", "", "")
	loginBase := fs.String("base-url", base, "")
	profile := fs.String("profile", o.profile, "")
	access := fs.String("aws-access-key-id", "", "")
	secret := fs.String("aws-secret-access-key", "", "")
	token := fs.String("aws-session-token", "", "")
	region := fs.String("aws-region", "", "")
	bedrock := fs.String("bedrock-model", "", "")
	if err := fs.Parse(o.args); err != nil {
		return err
	}
	if *profile == "" && *access == "" && *secret == "" {
		fmt.Println("Configure AWS Bedrock credentials for Opus.")
		*access = prompt("AWS Access Key ID: ")
		*secret = prompt("AWS Secret Access Key: ")
		*token = prompt("AWS Session Token (optional): ")
		*region = prompt("AWS Region [us-east-1]: ")
		if *region == "" {
			*region = "us-east-1"
		}
	}
	if *loginBase == "" {
		*loginBase = model.DefaultBaseURL
	}
	providerName := "opus"
	id := uuid.NewString()
	cfg := &model.Config{APIKey: *api, BaseURL: ptr(*loginBase), AWSProfile: nonempty(*profile), AWSAccessKeyID: nonempty(*access), AWSSecretAccessKey: nonempty(*secret), AWSSessionToken: nonempty(*token), AWSRegion: nonempty(*region), BedrockModel: nonempty(*bedrock), InstallationID: &id, ProjectProfiles: map[string]model.ProjectTrustProfile{}, ModelProvider: &providerName}
	bedrockClient := &provider.Bedrock{Config: cfg, Client: &http.Client{}}
	if err := bedrockClient.Verify(ctx); err != nil {
		if !looksLikeUseCaseError(err.Error()) {
			return fmt.Errorf("verify Bedrock Opus access: %w", err)
		}
		if err := submitAnthropicUseCase(ctx, cfg); err != nil {
			return fmt.Errorf("submit Anthropic Bedrock use-case form: %w", err)
		}
		time.Sleep(3 * time.Second)
		if err := bedrockClient.Verify(ctx); err != nil {
			return fmt.Errorf("verify Bedrock Opus access after use-case submission: %w", err)
		}
	}
	if err := appconfig.Save(cfg); err != nil {
		return err
	}
	f, _ := appconfig.File()
	fmt.Println("Saved config to " + f)
	return ui.Run(app.New(cfg, debug), nil, o.alt && !o.noAlt)
}

func looksLikeUseCaseError(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "use case details") || strings.Contains(lowered, "required use-case form") || strings.Contains(lowered, "ftuformnotfilled")
}

func submitAnthropicUseCase(ctx context.Context, cfg *model.Config) error {
	file, err := os.CreateTemp("", "yolomancer-bedrock-use-case-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	form := `{"companyName":"yolomancer training","companyWebsite":"https://example.com","intendedUsers":"0","industryOption":"Technology","otherIndustryOption":"","useCases":"Use Anthropic models on Amazon Bedrock for agentic software development, code assistance, workflow automation, and tool-using AI agents."}`
	if _, err := file.WriteString(form); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	args := []string{"bedrock", "put-use-case-for-model-access", "--region", appconfig.Region(cfg), "--form-data", "fileb://" + path}
	cmd := exec.CommandContext(ctx, "aws", args...)
	if err := provider.PrepareBedrock(ctx, cfg); err != nil {
		return err
	}
	creds, err := cfg.BedrockCredentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	bedrockConfig := *cfg
	bedrockConfig.AWSProfile = nil
	bedrockConfig.AWSAccessKeyID = &creds.AccessKeyID
	bedrockConfig.AWSSecretAccessKey = &creds.SecretAccessKey
	bedrockConfig.AWSSessionToken = &creds.SessionToken
	cmd.Env = provider.ToolEnvironment(&bedrockConfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func resume(ctx context.Context, o options, base string, debug bool) error {
	cfg, err := loadConfig(ctx, o)
	if err != nil {
		return err
	}
	if base != "" {
		cfg.BaseURL = &base
	}
	all := false
	var id string
	for _, a := range o.args {
		if a == "--all" {
			if all {
				return errors.New("the argument '--all' cannot be used multiple times")
			}
			all = true
		} else {
			if id != "" {
				return fmt.Errorf("unexpected argument `%s`", a)
			}
			id = a
		}
	}
	var snap *model.SessionSnapshot
	if id != "" {
		snap, err = session.Load(id)
	} else {
		cwd, _ := os.Getwd()
		var list []session.Summary
		list, err = session.List(all, cwd)
		if err == nil && len(list) == 0 {
			if all {
				return errors.New("No saved sessions found.")
			}
			return fmt.Errorf("No saved sessions found for current workspace %s. Try `yolomancer resume --all`.", cwd)
		}
		if err == nil {
			chosen := 0
			if len(list) > 1 && isTerminal(os.Stdin) {
				for i, s := range list {
					fmt.Printf("%d) %s  %s\n", i+1, short(s.SessionID), s.Preview)
				}
				raw := prompt("Resume session [1]: ")
				if raw != "" {
					fmt.Sscanf(raw, "%d", &chosen)
					chosen--
					if chosen < 0 || chosen >= len(list) {
						return errors.New("selection cancelled")
					}
				}
			}
			snap, err = session.Load(list[chosen].SessionID)
		}
	}
	if err != nil {
		return err
	}
	current, err := os.Getwd()
	if err != nil {
		return err
	}
	chosen, err := chooseResumeCWD(snap, current)
	if err != nil {
		return err
	}
	if snap.CWD == nil || *snap.CWD != chosen {
		snap.CWD = &chosen
		if !containsString(snap.CWDHistory, chosen) {
			snap.CWDHistory = append(snap.CWDHistory, chosen)
		}
		session.Touch(snap)
		if err := session.Write(snap); err != nil {
			return err
		}
	}
	if err = os.Chdir(chosen); err != nil {
		return err
	}
	return ui.Run(app.Restore(cfg, debug, snap), snap, o.alt && !o.noAlt)
}

func chooseResumeCWD(snap *model.SessionSnapshot, current string) (string, error) {
	dirs := append([]string{}, session.Dirs(snap)...)
	if !containsString(dirs, current) {
		dirs = append(dirs, current)
	}
	usable := dirs[:0]
	for _, dir := range dirs {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			usable = append(usable, dir)
		}
	}
	if len(usable) == 0 {
		return "", fmt.Errorf("session %s has no usable workspace directories", snap.SessionID)
	}
	if len(usable) == 1 || snap.CWD != nil && *snap.CWD == current {
		return map[bool]string{true: current, false: usable[0]}[snap.CWD != nil && *snap.CWD == current], nil
	}
	if !isTerminal(os.Stdin) {
		if snap.CWD != nil && containsString(usable, *snap.CWD) {
			return *snap.CWD, nil
		}
		return usable[0], nil
	}
	for i, dir := range usable {
		suffix := ""
		if dir == current {
			suffix = " (current)"
		} else if snap.CWD != nil && dir == *snap.CWD {
			suffix = " (saved)"
		}
		fmt.Printf("%d) %s%s\n", i+1, dir, suffix)
	}
	choice := 1
	raw := prompt("Resume workspace [1]: ")
	if raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &choice); err != nil || choice < 1 || choice > len(usable) {
			return "", errors.New("selection cancelled")
		}
	}
	return usable[choice-1], nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

type stdoutSink struct{ debug bool }

func (s stdoutSink) ReasoningDelta(v string) {
	if s.debug {
		fmt.Fprint(os.Stderr, v)
	}
}
func (s stdoutSink) AssistantDelta(v string)   { fmt.Print(v) }
func (s stdoutSink) AssistantMessage(v string) { fmt.Print(v) }
func (s stdoutSink) AssistantDone()            { fmt.Println() }
func (s stdoutSink) ToolCall(c model.ToolCall) { fmt.Fprintf(os.Stderr, "→ %s\n", c.Name) }
func (s stdoutSink) ToolResult(c model.ToolCall, v string) {
	fmt.Fprintf(os.Stderr, "← %s %s\n", c.Name, shortText(v, 240))
}
func (s stdoutSink) Info(v string) { fmt.Fprintln(os.Stderr, v) }
func (s stdoutSink) Debug(v string) {
	if s.debug {
		fmt.Fprintln(os.Stderr, "debug:", v)
	}
}
func (s stdoutSink) Usage(model.Usage) {}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Agentic coding CLI for yolomancer

Usage: yolomancer [OPTIONS] [COMMAND]

Commands:
  git     Manage shared S3 Git repositories and install native Git helpers
  register Claim workshop credentials using an access code
  login   Store AWS Bedrock credentials and optional defaults in ~/.yolomancer/config.toml
  logout  Remove stored credentials from ~/.yolomancer/config.toml
  run     Run a one-shot prompt
  resume  Resume a saved interactive session
  help    Print this message or the help of the given subcommand(s)

Options:
      --profile <PROFILE>  AWS profile for tools and Bedrock role assumption
      --debug
      --base-url <BASE_URL>
      --local
      --no-alt-screen
      --alt-screen
  -h, --help       Print help
  -V, --version    Print version`)
}

func commandUsage(w io.Writer, command string) {
	switch command {
	case "register":
		fmt.Fprintln(w, `Claim workshop credentials and store them in ~/.yolomancer/config.toml

Usage: yolomancer register <CODE> [--endpoint <HTTPS_URL>]

The default endpoint is https://vendor.yolomancer.com/claim.
The signing key is saved before claiming and reused for retries.
Existing preferences are retained; the selected AWS credentials are replaced.
Keep ~/.yolomancer/registration-identity.json for retries. Codes are not saved.
Access codes passed on the command line may be retained in shell history.`)
	case "login":
		fmt.Fprintln(w, `Store AWS Bedrock credentials and optional defaults in ~/.yolomancer/config.toml

Usage: yolomancer login [OPTIONS]

Options:
      --api-key <API_KEY>
      --debug
      --base-url <BASE_URL>
      --local
      --profile <PROFILE>
      --aws-access-key-id <AWS_ACCESS_KEY_ID>
      --no-alt-screen
      --alt-screen
      --aws-secret-access-key <AWS_SECRET_ACCESS_KEY>
      --aws-session-token <AWS_SESSION_TOKEN>
      --aws-region <AWS_REGION>
      --bedrock-model <BEDROCK_MODEL>
  -h, --help  Print help`)
	case "logout":
		fmt.Fprintln(w, "Remove stored credentials from ~/.yolomancer/config.toml\n\nUsage: yolomancer logout [OPTIONS]\n\nOptions:\n      --profile <PROFILE>\n      --debug\n      --base-url <BASE_URL>\n      --local\n      --no-alt-screen\n      --alt-screen\n  -h, --help  Print help")
	case "run":
		fmt.Fprintln(w, "Run a one-shot prompt\n\nUsage: yolomancer run [OPTIONS] <PROMPT>\n\nArguments:\n  <PROMPT>\n\nOptions:\n      --profile <PROFILE>\n      --debug\n      --base-url <BASE_URL>\n      --local\n      --no-alt-screen\n      --alt-screen\n  -h, --help  Print help")
	case "resume":
		fmt.Fprintln(w, "Resume a saved interactive session\n\nUsage: yolomancer resume [OPTIONS] [SESSION_ID]\n\nArguments:\n  [SESSION_ID]  Session id. If omitted, choose from sessions for the current workspace\n\nOptions:\n      --profile <PROFILE>\n      --all                  Show sessions from all workspaces when choosing interactively\n      --debug\n      --base-url <BASE_URL>\n      --local\n      --no-alt-screen\n      --alt-screen\n  -h, --help  Print help")
	default:
		usage(w)
	}
}
func prompt(label string) string {
	fmt.Print(label)
	v, _ := inputReader.ReadString('\n')
	return strings.TrimSpace(v)
}
func nonempty(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}
func ptr(v string) *string { return &v }
func isTerminal(f *os.File) bool {
	st, e := f.Stat()
	return e == nil && (st.Mode()&os.ModeCharDevice) != 0
}
func short(v string) string {
	if len(v) > 8 {
		return v[:8]
	}
	return v
}
func shortText(v string, n int) string {
	v = strings.ReplaceAll(v, "\n", " ")
	if len(v) > n {
		return v[:n] + "..."
	}
	return v
}
func envDebug() bool {
	for _, k := range []string{"yolomancer_debug", "YOLOMANCER_DEBUG", "VIBECODE_DEBUG"} {
		switch strings.ToLower(os.Getenv(k)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

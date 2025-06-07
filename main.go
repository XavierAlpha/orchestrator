package main

import (
	"encoding/csv"
	"github.com/spf13/viper"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GlobalCfg struct {
	DefaultWorkspaceDir string `mapstructure:"default_workspace_dir"`
	DefaultGoVersion    string `mapstructure:"default_go_version"`
}
type RepoCfg struct {
	Workspace string `mapstructure:"workspace"`
	URL       string `mapstructure:"url"`
	GoVer     string `mapstructure:"build_go_version"`
	Commits   string `mapstructure:"build_commits"`
	Branches  string `mapstructure:"build_branches"`
	Plats     string `mapstructure:"build_platforms"`
	Tags      string `mapstructure:"build_tags"`
	Ldflags   string `mapstructure:"build_ldflags"`
	Envs      string `mapstructure:"envs"`
}
type Root struct {
	Globals GlobalCfg          `mapstructure:"globals"`
	Assets  map[string]RepoCfg `mapstructure:"assets"`
}

func splitCSV(s string) []string {
	r, _ := csv.NewReader(strings.NewReader(s)).Read()
	var out []string
	for _, v := range r {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func run(dir string, env []string, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func repl(s, sha, tag, out string) string {
	return strings.NewReplacer(
		"${SHORT_SHA}", sha,
		"${VERSION_TAG}", tag,
		"${OUTPUT}", out,
	).Replace(s)
}

func compile(repo, ref, plat string, rc RepoCfg, g GlobalCfg) error {
	workspace := os.ExpandEnv(rc.Workspace)
	if workspace == "" {
		workspace = g.DefaultWorkspaceDir
	}
	repoDir := filepath.Join(workspace, repo)
	if !exists(repoDir) {
		if err := os.MkdirAll(workspace, 0755); err != nil {
			log.Fatalf("[%s] cannot create workspace: %v", repo, err)
		}
	}

	if err := run(repoDir, nil, "git", "checkout", ref); err != nil {
		return err
	}
	shortSHA, _ := git(repoDir, "rev-parse", "--short=7", "HEAD")
	tag, _ := git(repoDir, "describe", "--tags", "--abbrev=0")

	outDir := filepath.Join(workspace, repo, ref, plat)
	_ = os.MkdirAll(outDir, 0755)
	outFile := filepath.Join(outDir, repo)

	env := os.Environ()
	osArch := strings.SplitN(plat, "/", 2)
	env = append(env,
		"GOOS="+osArch[0],
		"GOARCH="+osArch[1],
		"CGO_ENABLED=0",
		"SHORT_SHA="+shortSHA,
		"VERSION_TAG="+tag,
		"OUTPUT="+outFile,
	)
	for _, kv := range splitCSV(os.ExpandEnv(rc.Envs)) {
		env = append(env, kv)
	}

	args := []string{"build", "-v", "-trimpath", "-buildvcs=false"}
	if rc.Tags != "" {
		args = append(args, "-tags", repl(rc.Tags, shortSHA, tag, outFile))
	}
	if rc.Ldflags != "" {
		args = append(args, "-ldflags", repl(rc.Ldflags, shortSHA, tag, outFile))
	}
	args = append(args, "-o", outFile, "./...")

	log.Printf("→ %s %v", "go", args)
	return run(repoDir, env, "go", args...)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cloneOrFetch(dir, url string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return run(dir, nil, "git", "fetch", "--all", "--prune")
	}
	return run(filepath.Dir(dir), nil, "git", "clone", "--depth=1", url, dir)
}

func main() {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err != nil {
		log.Fatal(err)
	}
	var root Root
	if err := v.Unmarshal(&root); err != nil {
		log.Fatal(err)
	}

	repo := mustEnv("REPO")
	ref := mustEnv("REF")
	plat := mustEnv("PLATFORM")

	rc, ok := root.Assets[repo]
	if !ok {
		log.Fatalf("repo %s not in config", repo)
	}

	if err := compile(repo, ref, plat, rc, root.Globals); err != nil {
		log.Fatal("build failed:", err)
	}
	log.Println("✔︎ done")
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env %s", k)
	}
	return v
}

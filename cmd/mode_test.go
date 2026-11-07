package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// mode 决定运行哪棵 queries.* 树，而 gov 是会对标识符做哈希的那一个。
// onprem 是三者中保护最弱的，因此向它回退是这里唯一可能犯的错：
// `-mode govv` 过去只打印一行，然后采集未经哈希的 onprem bundle，而在
// stdin 关闭时——任何 CI 任务——都没有人读那一行。

func TestCanonicalMode_NormalisesAndAliases(t *testing.T) {
	for in, want := range map[string]string{
		"gov": "gov", "GOV": "gov", " gov ": "gov", "\tGov\n": "gov",
		"cloud": "cloud", "CLOUD": "cloud", "ch-cloud": "cloud", "clickhouse-cloud": "cloud",
		"onprem": "onprem", "on-prem": "onprem", "on_prem": "onprem",
		"on-premise": "onprem", "on-premises": "onprem", "self-hosted": "onprem",
		// 无法识别的必须为 ""，绝不能猜测。
		"govv": "", "": "", "  ": "", "prod": "", "government": "",
	} {
		if got := canonicalMode(in); got != want {
			t.Errorf("canonicalMode(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 带空白的写法单独列出，因为它是过去会被漏判的那种：
// ToLower 保留了空格，== "gov" 失败，于是采用了 onprem。
func TestCanonicalMode_PaddedGovIsNotDowngraded(t *testing.T) {
	if got := canonicalMode(" gov"); got != "gov" {
		t.Fatalf(`canonicalMode(" gov") = %q——带空白的 gov 不得漏判`, got)
	}
}

func TestResolveMode_ExplicitInvalidIsAnError(t *testing.T) {
	for _, raw := range []string{"govv", "onpre", "prod", " ", "cloud9"} {
		mode, _, err := resolveMode(raw, true, false, unusedPrompt(t))
		if err == nil {
			t.Errorf("resolveMode(%q, explicit) 返回 %q 且无错误——无法解析的 mode 必须报错，而不是取默认值", raw, mode)
		}
		if mode != "" {
			t.Errorf("resolveMode(%q, explicit) 在报错的同时泄露了 mode %q", raw, mode)
		}
	}
}

func TestResolveMode_SuggestsTheNearestMode(t *testing.T) {
	_, _, err := resolveMode("govv", true, false, unusedPrompt(t))
	if err == nil || !strings.Contains(err.Error(), `你是指 "gov" 吗？`) {
		t.Fatalf("期望得到 gov 建议，实际得到 %v", err)
	}
	// 没有足够接近的候选时不应凭空编造。
	_, _, err = resolveMode("banana", true, false, unusedPrompt(t))
	if err == nil || strings.Contains(err.Error(), "你是指") {
		t.Fatalf("无关输入不应给出建议，实际得到 %v", err)
	}
}

func TestResolveMode_ExplicitValidPassesThrough(t *testing.T) {
	for raw, want := range map[string]string{
		"gov": "gov", " GOV ": "gov", "cloud": "cloud", "self-hosted": "onprem",
	} {
		got, notice, err := resolveMode(raw, true, false, unusedPrompt(t))
		if err != nil || got != want {
			t.Errorf("resolveMode(%q) = (%q, %v)，期望 %q", raw, got, err, want)
		}
		if notice != "" {
			t.Errorf("resolveMode(%q) 不应为显式选择附加说明，实际得到 %q", raw, notice)
		}
	}
}

// 未设置且无人可问：默认值仍然生效，但会予以通告。
// 这是 CI 的形态，而通告是运维人员唯一能得到的信号。
func TestResolveMode_UnsetNonInteractiveDefaultsAndSaysSo(t *testing.T) {
	got, notice, err := resolveMode("onprem", false, false, unusedPrompt(t))
	if err != nil || got != "onprem" {
		t.Fatalf("resolveMode(未设置, 非交互式) = (%q, %v)，期望 onprem", got, err)
	}
	if !strings.Contains(notice, "onprem") || !strings.Contains(notice, "不是终端") {
		t.Errorf("必须通告默认值，实际得到 %q", notice)
	}
}

func TestResolveMode_UnsetInteractive(t *testing.T) {
	// 回车接受所提供的默认值。
	got, _, err := resolveMode("onprem", false, true, func() (string, error) { return "\n", nil })
	if err != nil || got != "onprem" {
		t.Errorf("空答案应采用默认值，实际得到 (%q, %v)", got, err)
	}
	// 输入的 mode 优先，包括别名和空白。
	got, _, err = resolveMode("onprem", false, true, func() (string, error) { return "  GOV \n", nil })
	if err != nil || got != "gov" {
		t.Errorf("输入的答案应被采纳，实际得到 (%q, %v)", got, err)
	}
	// 提示符处的拼写错误同样报错——理由与标志相同。
	if _, _, err = resolveMode("onprem", false, true, func() (string, error) { return "govv\n", nil }); err == nil {
		t.Error("拼错的答案不得回退到 onprem")
	}
	// 未输入时按 Ctrl-D 是失败，而不是同意默认值。
	if _, _, err = resolveMode("onprem", false, true, func() (string, error) { return "", io.EOF }); err == nil {
		t.Error("无法读取的提示必须失败，而不是假定为 onprem")
	}
}

func TestGetQueriesDir(t *testing.T) {
	for mode, want := range map[string]string{
		"cloud": "./queries.cloud", "onprem": "./queries.onprem", "gov": "./queries.gov",
		" GOV ": "./queries.gov", "self-hosted": "./queries.onprem",
	} {
		got, err := getQueriesDir(mode)
		if err != nil || got != want {
			t.Errorf("getQueriesDir(%q) = (%q, %v)，期望 %q", mode, got, err, want)
		}
	}
	// 旧的默认值在这里返回 ./queries.onprem，使这里成为无法识别的 mode
	// 变成未哈希采集的第二处。
	if got, err := getQueriesDir("govv"); err == nil {
		t.Errorf("getQueriesDir(%q) 必须报错而不是回退，实际得到 %q", "govv", got)
	}
}

// 如果解析器询问了本不该询问的问题，unusedPrompt 会让测试失败。
func unusedPrompt(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Error("resolveMode 在本不该提示时进行了提示")
		return "", errors.New("unexpected prompt")
	}
}

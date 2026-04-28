package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/golang/glog"
)

var (
	qwenModelPhrasePattern   = regexp.MustCompile(`(?i)qwen[0-9a-z_-]*\s*(模型|model)`)
	doubaoModelPhrasePattern = regexp.MustCompile(`(?i)doubao\s*(模型|model)`)
	qwenNamePattern          = regexp.MustCompile(`(?i)qwen[0-9a-z_-]*`)
	doubaoNamePattern        = regexp.MustCompile(`(?i)doubao`)
	sessionParenPattern      = regexp.MustCompile(`(?i)Session\s*\(\s*ID\s*=\s*([^\)\s]+)\s*\)`)
	sessionKVPattern         = regexp.MustCompile(`(?i)\b(session_id|sessionid|session)\b(\s*[:=]\s*)([^\s,\)\]]+)`)
	connectionKVPattern      = regexp.MustCompile(`(?i)\b(connection_id|connectionid|connection)\b(\s*[:=]\s*)([^\s,\)\]]+)`)
)

func publicModelAliasInText(text string) string {
	text = qwenModelPhrasePattern.ReplaceAllString(text, "模型二")
	text = doubaoModelPhrasePattern.ReplaceAllString(text, "模型一")
	text = qwenNamePattern.ReplaceAllString(text, "模型二")
	text = doubaoNamePattern.ReplaceAllString(text, "模型一")
	return text
}

func redactSessionID(id string) string {
	return redactIdentifier(id, "sess")
}

func redactConnectionID(id string) string {
	return redactIdentifier(id, "conn")
}

func redactIdentifier(value, prefix string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "\"'")
	if trimmed == "" {
		return prefix + "_redacted"
	}
	if isAlreadyRedactedIdentifier(trimmed, prefix) {
		return strings.ToLower(trimmed)
	}

	digest := sha256.Sum256([]byte(trimmed))
	return prefix + "_" + hex.EncodeToString(digest[:4])
}

func isAlreadyRedactedIdentifier(value, prefix string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == prefix+"_redacted" {
		return true
	}
	if !strings.HasPrefix(normalized, prefix+"_") {
		return false
	}
	suffix := strings.TrimPrefix(normalized, prefix+"_")
	if len(suffix) != 8 {
		return false
	}
	for _, c := range suffix {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func sanitizeUserVisibleLog(text string) string {
	if text == "" {
		return text
	}

	text = sessionParenPattern.ReplaceAllStringFunc(text, func(match string) string {
		submatch := sessionParenPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		return "Session (ID=" + redactSessionID(submatch[1]) + ")"
	})

	text = sessionKVPattern.ReplaceAllStringFunc(text, func(match string) string {
		submatch := sessionKVPattern.FindStringSubmatch(match)
		if len(submatch) < 4 {
			return match
		}
		return submatch[1] + submatch[2] + redactSessionID(submatch[3])
	})

	text = connectionKVPattern.ReplaceAllStringFunc(text, func(match string) string {
		submatch := connectionKVPattern.FindStringSubmatch(match)
		if len(submatch) < 4 {
			return match
		}
		return submatch[1] + submatch[2] + redactConnectionID(submatch[3])
	})

	return publicModelAliasInText(text)
}

func safeInfo(args ...interface{}) {
	glog.Info(sanitizeUserVisibleLog(fmt.Sprint(args...)))
}

func safeInfof(format string, args ...interface{}) {
	glog.Info(sanitizeUserVisibleLog(fmt.Sprintf(format, args...)))
}

func safeWarning(args ...interface{}) {
	glog.Warning(sanitizeUserVisibleLog(fmt.Sprint(args...)))
}

func safeWarningf(format string, args ...interface{}) {
	glog.Warning(sanitizeUserVisibleLog(fmt.Sprintf(format, args...)))
}

func safeError(args ...interface{}) {
	glog.Error(sanitizeUserVisibleLog(fmt.Sprint(args...)))
}

func safeErrorf(format string, args ...interface{}) {
	glog.Error(sanitizeUserVisibleLog(fmt.Sprintf(format, args...)))
}

type safeVerboseLogger struct {
	verbose glog.Verbose
}

func safeV(level glog.Level) safeVerboseLogger {
	return safeVerboseLogger{verbose: glog.V(level)}
}

func (v safeVerboseLogger) Info(args ...interface{}) {
	if !v.verbose {
		return
	}
	v.verbose.Info(sanitizeUserVisibleLog(fmt.Sprint(args...)))
}

func (v safeVerboseLogger) Infof(format string, args ...interface{}) {
	if !v.verbose {
		return
	}
	v.verbose.Info(sanitizeUserVisibleLog(fmt.Sprintf(format, args...)))
}

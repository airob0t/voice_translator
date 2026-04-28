package main

import (
	"bytes"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var (
	VirtualMicRouteDevice = ""
)

// AudioDevice represents an audio device
type AudioDevice struct {
	ID   string
	Name string
}

// isVirtualAudioDevice checks if a device name matches virtual audio devices
func isVirtualAudioDevice(name string) bool {
	// List of virtual audio device patterns to exclude
	virtualDevicePatterns := []string{
		"Translator Audio Device",
		"BlackHole",
		"Virtual",
		"Loopback",
		"VB-Audio",
		"DeviceName",
		"星译音频",
	}

	nameLower := strings.ToLower(name)
	for _, pattern := range virtualDevicePatterns {
		if strings.Contains(nameLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// GetAudioInputDevices returns a list of available audio input devices
func GetAudioInputDevices() []AudioDevice {
	var cmd *exec.Cmd
	var stderr bytes.Buffer

	if runtime.GOOS == "windows" {
		// Windows: use DirectShow
		cmd = exec.Command(".\\ffmpeg.exe", "-f", "dshow", "-list_devices", "true", "-i", "dummy")
	} else {
		// macOS: use AVFoundation
		// setupbundle会设置PATH，所以不需要相对路径
		cmd = exec.Command("ffmpeg", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	}

	cmd.Stderr = &stderr
	cmd.Run() // This command will fail, but we need the stderr output

	output := stderr.String()
	safeV(2).Infof("Audio input devices raw output:\n%s", output)
	return parseAudioDevices(output, true)
}

// GetAudioOutputDevices returns a list of available audio output devices
func GetAudioOutputDevices() []AudioDevice {
	var cmd *exec.Cmd

	if runtime.GOOS == "windows" {
		cmd = exec.Command(".\\mpv.com", "--audio-device=help")
	} else {
		// setupbundle会设置PATH，所以不需要相对路径
		cmd = exec.Command("mpv", "--audio-device=help")
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run()

	output := stdout.String()
	safeV(2).Infof("Audio output devices raw output:\n%s", output)
	return parseMpvAudioDevices(output)
}

// parseAudioDevices parses the ffmpeg device list output
func parseAudioDevices(output string, isInput bool) []AudioDevice {
	var devices []AudioDevice
	lines := strings.Split(output, "\n")

	if runtime.GOOS == "windows" {
		// Windows DirectShow format
		// Example: [dshow @ 0x...] "Microphone (Realtek High Definition Audio)" (audio)
		deviceRegex := regexp.MustCompile(`\[dshow[^\]]*\]\s+"([^"]+)"\s+\(audio\)`)

		for _, line := range lines {
			matches := deviceRegex.FindStringSubmatch(line)
			if len(matches) >= 2 {
				name := strings.TrimSpace(matches[1])
				safeV(3).Infof("Found audio device: %s", name)
				// Skip virtual audio devices
				if !isVirtualAudioDevice(name) {
					devices = append(devices, AudioDevice{
						ID:   "audio=" + name,
						Name: name,
					})
					safeV(2).Infof("Added audio input device: %s", name)
				} else {
					safeV(2).Infof("Skipped virtual audio device: %s", name)
				}
			}
		}
	} else {
		// macOS AVFoundation format
		// Example: [AVFoundation indev @ 0x...] [0] FaceTime HD Camera
		// Example: [AVFoundation indev @ 0x...] [1] MacBook Pro Microphone
		deviceRegex := regexp.MustCompile(`\[AVFoundation.*\] \[(\d+)\] (.+)`)

		inInputSection := false
		inOutputSection := false

		for _, line := range lines {
			if strings.Contains(line, "AVFoundation audio devices:") {
				inInputSection = true
				inOutputSection = false
				continue
			}
			if strings.Contains(line, "AVFoundation video devices:") {
				inInputSection = false
				continue
			}

			if !inInputSection && !inOutputSection {
				continue
			}

			matches := deviceRegex.FindStringSubmatch(line)
			if len(matches) >= 3 {
				id := matches[1]
				name := strings.TrimSpace(matches[2])
				// Skip virtual audio devices
				if !isVirtualAudioDevice(name) {
					devices = append(devices, AudioDevice{
						ID:   ":" + id,
						Name: name,
					})
				}
			}
		}
	}

	safeInfof("Found %d audio input devices", len(devices))
	return devices
}

// parseMpvAudioDevices parses the mpv audio device list output
func parseMpvAudioDevices(output string) []AudioDevice {
	var devices []AudioDevice
	lines := strings.Split(output, "\n")

	var deviceRegex *regexp.Regexp
	if runtime.GOOS == "windows" {
		// Windows WASAPI format
		// Example:   'wasapi/{device-guid}' (Speakers (Realtek High Definition Audio))
		deviceRegex = regexp.MustCompile(`^\s+'(wasapi/[^']+)'\s+\((.+)\)`)
	} else {
		// macOS CoreAudio format
		// Example:   'coreaudio/AppleUSBAudioEngine:...' (USB Audio Device)
		// Example:   'coreaudio/BuiltInSpeakerDevice' (Built-in Output)
		deviceRegex = regexp.MustCompile(`^\s+'(coreaudio/[^']+)'\s+\((.+)\)`)
	}

	for _, line := range lines {
		matches := deviceRegex.FindStringSubmatch(line)
		if len(matches) >= 3 {
			id := matches[1]
			name := strings.TrimSpace(matches[2])
			safeV(3).Infof("Found output device: %s (%s)", name, id)
			// Skip virtual audio devices
			if !isVirtualAudioDevice(name) {
				devices = append(devices, AudioDevice{
					ID:   id,
					Name: name,
				})
				safeV(2).Infof("Added audio output device: %s", name)
			} else {
				safeV(2).Infof("Skipped virtual audio output device: %s", name)
				if strings.Contains(name, "CABLE-A Input") {
					VirtualMicRouteDevice = id
				}
			}
		}
	}

	safeInfof("Found %d audio output devices", len(devices))
	return devices
}

package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/data/binding"
	fyneTest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

type blockingStringBinding struct {
	mu sync.Mutex

	value           string
	setCallCount    int
	firstSetEntered chan struct{}
	releaseFirstSet chan struct{}
}

func newBlockingStringBinding() *blockingStringBinding {
	return &blockingStringBinding{
		firstSetEntered: make(chan struct{}),
		releaseFirstSet: make(chan struct{}),
	}
}

func (b *blockingStringBinding) AddListener(_ binding.DataListener) {}

func (b *blockingStringBinding) RemoveListener(_ binding.DataListener) {}

func (b *blockingStringBinding) Get() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value, nil
}

func (b *blockingStringBinding) Set(v string) error {
	b.mu.Lock()
	b.setCallCount++
	currentSetCall := b.setCallCount
	b.mu.Unlock()

	if currentSetCall == 1 {
		close(b.firstSetEntered)
		<-b.releaseFirstSet
	}

	b.mu.Lock()
	b.value = v
	b.mu.Unlock()
	return nil
}

func TestAddStatusKeepsBothConcurrentMessages(t *testing.T) {
	b := newBlockingStringBinding()
	ui := &TranslationUI{
		statusString: b,
	}

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		ui.addStatus("first")
		close(firstDone)
	}()

	select {
	case <-b.firstSetEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first Set call")
	}

	go func() {
		ui.addStatus("second")
		close(secondDone)
	}()

	time.Sleep(50 * time.Millisecond)
	close(b.releaseFirstSet)

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first addStatus call")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second addStatus call")
	}

	got, err := b.Get()
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expected both messages to be present, got %q", got)
	}
}

func TestAppendSourceTextKeepsBothConcurrentMessages(t *testing.T) {
	origDo := fyneDo
	defer func() {
		fyneDo = origDo
	}()
	fyneDo = func(func()) {}

	b := newBlockingStringBinding()
	ui := &TranslationUI{
		sourceString: b,
		sourceText:   widget.NewRichTextFromMarkdown(""),
	}

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		ui.appendSourceText("first")
		close(firstDone)
	}()

	select {
	case <-b.firstSetEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first Set call")
	}

	go func() {
		ui.appendSourceText("second")
		close(secondDone)
	}()

	time.Sleep(50 * time.Millisecond)
	close(b.releaseFirstSet)

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first appendSourceText call")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second appendSourceText call")
	}

	got, err := b.Get()
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expected both messages to be present, got %q", got)
	}
}

func TestAppendTranslationTextKeepsBothConcurrentMessages(t *testing.T) {
	origDo := fyneDo
	defer func() {
		fyneDo = origDo
	}()
	fyneDo = func(func()) {}

	b := newBlockingStringBinding()
	ui := &TranslationUI{
		translationString: b,
		translationText:   widget.NewRichTextFromMarkdown(""),
	}

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		ui.appendTranslationText("first")
		close(firstDone)
	}()

	select {
	case <-b.firstSetEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first Set call")
	}

	go func() {
		ui.appendTranslationText("second")
		close(secondDone)
	}()

	time.Sleep(50 * time.Millisecond)
	close(b.releaseFirstSet)

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first appendTranslationText call")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second appendTranslationText call")
	}

	got, err := b.Get()
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expected both messages to be present, got %q", got)
	}
}

func TestFinalizeRunIfCurrentIgnoresStaleRun(t *testing.T) {
	ui := &TranslationUI{
		running:         true,
		activeRunSerial: 2,
		cancelFunc:      func() {},
	}

	if ui.finalizeRunIfCurrent(1, false) {
		t.Fatal("expected stale run finalization to be ignored")
	}
	if !ui.running {
		t.Fatal("stale run should not change running state")
	}
	if ui.cancelFunc == nil {
		t.Fatal("stale run should not clear current cancel function")
	}

	if !ui.finalizeRunIfCurrent(2, false) {
		t.Fatal("expected active run finalization to succeed")
	}
	if ui.running {
		t.Fatal("active run finalization should set running=false")
	}
	if ui.cancelFunc != nil {
		t.Fatal("active run finalization should clear cancel function")
	}
}

func TestFinalizeRunIfCurrentClearsBidirectionalState(t *testing.T) {
	ui := &TranslationUI{
		running:           true,
		micRunning:        true,
		speakerRunning:    true,
		mainContext:       context.Background(),
		micCancelFunc:     func() {},
		speakerCancelFunc: func() {},
		activeRunSerial:   3,
	}

	if !ui.finalizeRunIfCurrent(3, true) {
		t.Fatal("expected bidirectional run finalization to succeed")
	}
	if ui.running {
		t.Fatal("expected running=false after finalization")
	}
	if ui.micRunning || ui.speakerRunning {
		t.Fatal("expected mic/speaker running flags to be cleared")
	}
	if ui.mainContext != nil {
		t.Fatal("expected main context to be cleared")
	}
	if ui.micCancelFunc != nil || ui.speakerCancelFunc != nil {
		t.Fatal("expected component cancel funcs to be cleared")
	}
}

func TestShouldShowBidirectionalControlsRequiresBidirectionalReady(t *testing.T) {
	ui := &TranslationUI{
		mode:    ModeBidirectional,
		running: true,
	}

	ui.bidirectionalReady.Store(false)
	if ui.shouldShowBidirectionalControls() {
		t.Fatal("expected bidirectional toggles hidden before runtime is ready")
	}

	ui.bidirectionalReady.Store(true)
	if !ui.shouldShowBidirectionalControls() {
		t.Fatal("expected bidirectional toggles visible after runtime is ready")
	}
}

func TestOnMicToggleBeforeBidirectionalReadyDoesNotStart(t *testing.T) {
	ui := &TranslationUI{
		mode:            ModeBidirectional,
		running:         true,
		micToggleButton: widget.NewButton("启动麦克风", nil),
	}

	ui.bidirectionalReady.Store(false)
	ui.onMicToggle()

	if ui.micRunning {
		t.Fatal("expected mic to remain stopped before bidirectional runtime is ready")
	}
	if ui.micCancelFunc != nil {
		t.Fatal("expected no mic cancel func to be created before runtime is ready")
	}
	if ui.micToggleButton.Text != "启动麦克风" {
		t.Fatalf("unexpected mic toggle text: %q", ui.micToggleButton.Text)
	}
}

func TestOnMicToggleDoesNotFallbackToBackgroundContext(t *testing.T) {
	ui := &TranslationUI{
		mode:            ModeBidirectional,
		running:         true,
		micToggleButton: widget.NewButton("启动麦克风", nil),
	}

	ui.bidirectionalReady.Store(true)
	ui.onMicToggle()

	if ui.micRunning {
		t.Fatal("expected mic to stay stopped when main context is unavailable")
	}
	if ui.micCancelFunc != nil {
		t.Fatal("expected no mic cancel func when main context is unavailable")
	}
}

func TestPrepareBidirectionalControlsForStartResetsToggleTexts(t *testing.T) {
	app := fyneTest.NewApp()
	defer app.Quit()

	ui := &TranslationUI{
		micToggleButton:     widget.NewButton("启动麦克风", nil),
		speakerToggleButton: widget.NewButton("启动扬声器", nil),
	}

	ui.prepareBidirectionalControlsForStart()

	if ui.micToggleButton.Text != "停止麦克风" {
		t.Fatalf("unexpected mic toggle text: %q", ui.micToggleButton.Text)
	}
	if ui.speakerToggleButton.Text != "停止扬声器" {
		t.Fatalf("unexpected speaker toggle text: %q", ui.speakerToggleButton.Text)
	}
}

func TestShouldAbortRunForCanceledOrStaleRun(t *testing.T) {
	ui := &TranslationUI{
		activeRunSerial: 3,
	}

	if !ui.shouldAbortRun(context.Background(), 2) {
		t.Fatal("expected stale run to abort")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if !ui.shouldAbortRun(canceledCtx, 3) {
		t.Fatal("expected canceled context to abort")
	}

	if ui.shouldAbortRun(context.Background(), 3) {
		t.Fatal("expected current run with live context to continue")
	}
}

func TestOnStopInvalidatesActiveRunSerial(t *testing.T) {
	app := fyneTest.NewApp()
	defer app.Quit()

	canceled := false
	ui := &TranslationUI{
		mode:                ModeMicrophone,
		running:             true,
		runSerial:           8,
		activeRunSerial:     8,
		startButton:         widget.NewButton("启动", nil),
		stopButton:          widget.NewButton("停止", nil),
		micToggleButton:     widget.NewButton("停止麦克风", nil),
		speakerToggleButton: widget.NewButton("停止扬声器", nil),
		micSelect:           widget.NewSelect([]string{"麦克风"}, nil),
		speakerSelect:       widget.NewSelect([]string{"扬声器"}, nil),
		modeSelect:          widget.NewSelect([]string{"单麦克风模式"}, nil),
		modelSelect:         widget.NewSelect([]string{"模型二(流畅多语言和音色选择)"}, nil),
		sourceLangSelect:    widget.NewSelect([]string{"中文"}, nil),
		targetLangSelect:    widget.NewSelect([]string{"英语"}, nil),
		voiceSelect:         widget.NewSelect([]string{"普通话男"}, nil),
		statusString:        binding.NewString(),
		cancelFunc: func() {
			canceled = true
		},
	}

	ui.onStop()

	if !canceled {
		t.Fatal("expected active cancel function to be invoked")
	}
	if ui.activeRunSerial == 8 {
		t.Fatal("expected active run serial to be invalidated on stop")
	}
}

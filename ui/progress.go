package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
)

// ProgressBar wraps schollz/progressbar with a simpler interface
type ProgressBar struct {
	bar         *progressbar.ProgressBar
	description string
}

// NewProgressBar creates a new progress bar
func NewProgressBar(total int64, description string) *ProgressBar {
	bar := progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWriter(nil),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	return &ProgressBar{
		bar:         bar,
		description: description,
	}
}

// Set sets the current progress
func (p *ProgressBar) Set(current int64) {
	p.bar.Set64(current)
}

// Add adds to the current progress
func (p *ProgressBar) Add(n int64) {
	p.bar.Add64(n)
}

// Finish completes the progress bar
func (p *ProgressBar) Finish() {
	p.bar.Finish()
	fmt.Println()
}

// Clear clears the progress bar
func (p *ProgressBar) Clear() {
	p.bar.Clear()
}

// DownloadProgress creates a progress callback for downloads
func DownloadProgress(description string, total int64) func(downloaded, totalSize int64) {
	bar := NewProgressBar(total, description)

	return func(downloaded, totalSize int64) {
		if totalSize > 0 && totalSize != total {
			// Update total if it changed
			bar.bar.ChangeMax64(totalSize)
		}
		bar.Set(downloaded)

		if downloaded >= totalSize {
			bar.Finish()
		}
	}
}

// Spinner displays a spinner while a task is running
type Spinner struct {
	message   string
	done      chan bool
	running   bool
	chars     []string
	charIndex int
}

// NewSpinner creates a new spinner
func NewSpinner(message string) *Spinner {
	return &Spinner{
		message: message,
		done:    make(chan bool),
		chars:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	}
}

// Start starts the spinner
func (s *Spinner) Start() {
	s.running = true
	go func() {
		for {
			select {
			case <-s.done:
				return
			default:
				fmt.Printf("\r%s %s", s.chars[s.charIndex], s.message)
				s.charIndex = (s.charIndex + 1) % len(s.chars)
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

// Stop stops the spinner
func (s *Spinner) Stop() {
	if s.running {
		s.done <- true
		s.running = false
		fmt.Print("\r")
		fmt.Print(strings.Repeat(" ", len(s.message)+4))
		fmt.Print("\r")
	}
}

// Success stops the spinner and shows success
func (s *Spinner) Success(message string) {
	s.Stop()
	if message == "" {
		message = s.message
	}
	fmt.Printf("✓ %s\n", message)
}

// Fail stops the spinner and shows failure
func (s *Spinner) Fail(message string) {
	s.Stop()
	if message == "" {
		message = s.message
	}
	fmt.Printf("✗ %s\n", message)
}

// StepProgress tracks progress through numbered steps
type StepProgress struct {
	totalSteps   int
	currentStep  int
	stepMessages []string
}

// NewStepProgress creates a new step progress tracker
func NewStepProgress(totalSteps int) *StepProgress {
	return &StepProgress{
		totalSteps:   totalSteps,
		currentStep:  0,
		stepMessages: make([]string, 0),
	}
}

// Step advances to the next step
func (p *StepProgress) Step(message string) {
	p.currentStep++
	p.stepMessages = append(p.stepMessages, message)
	fmt.Printf("[%d/%d] %s...\n", p.currentStep, p.totalSteps, message)
}

// Complete marks a step as complete
func (p *StepProgress) Complete(message string) {
	if message == "" && len(p.stepMessages) > 0 {
		message = p.stepMessages[len(p.stepMessages)-1]
	}
	fmt.Printf("  ✓ %s\n", message)
}

// Error marks a step as failed
func (p *StepProgress) Error(message string) {
	fmt.Printf("  ✗ %s\n", message)
}

// DeploymentProgress tracks deployment progress with visual feedback
type DeploymentProgress struct {
	stages       []string
	currentStage int
	spinner      *Spinner
}

// NewDeploymentProgress creates a new deployment progress tracker
func NewDeploymentProgress() *DeploymentProgress {
	return &DeploymentProgress{
		stages: []string{
			"Validating configuration",
			"Preparing images",
			"Creating VMs",
			"Configuring networks",
			"Starting VMs",
		},
		currentStage: -1,
	}
}

// StartStage starts a new deployment stage
func (p *DeploymentProgress) StartStage(stage string) {
	// Stop previous spinner
	if p.spinner != nil {
		p.spinner.Success("")
	}

	p.currentStage++
	p.spinner = NewSpinner(stage)
	p.spinner.Start()
}

// CompleteStage completes the current stage
func (p *DeploymentProgress) CompleteStage() {
	if p.spinner != nil {
		p.spinner.Success("")
		p.spinner = nil
	}
}

// FailStage fails the current stage
func (p *DeploymentProgress) FailStage(message string) {
	if p.spinner != nil {
		p.spinner.Fail(message)
		p.spinner = nil
	}
}

// Log logs a message during deployment
func (p *DeploymentProgress) Log(message string) {
	// Temporarily stop spinner
	wasRunning := p.spinner != nil && p.spinner.running
	if wasRunning {
		p.spinner.Stop()
	}

	fmt.Printf("  %s\n", message)

	// Restart spinner
	if wasRunning && p.spinner != nil {
		p.spinner.Start()
	}
}

// Done completes deployment progress
func (p *DeploymentProgress) Done() {
	if p.spinner != nil {
		p.spinner.Success("")
	}
}

package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

func guardRows() []QueueRow {
	return []QueueRow{{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"}}
}

// TestRunningStopGuardArmsThenConfirms pins the in-session app shell's two-key stop:
// a first q arms the confirm without cancelling; a second q cancels the run.
func TestRunningStopGuardArmsThenConfirms(t *testing.T) {
	stopped := false
	m := runningApp(120, 30, guardRows())
	m.loopCancel = func() { stopped = true }

	nm, _ := m.handleRunningKey(keyPress("q"))
	m = nm.(appModel)
	if stopped {
		t.Fatal("first q must not cancel the run")
	}
	if !m.dash.stopArmed() {
		t.Fatal("first q must arm the stop confirm")
	}
	if m.dash.stopping {
		t.Fatal("first q must not mark the run stopping")
	}

	nm, _ = m.handleRunningKey(keyPress("q"))
	m = nm.(appModel)
	if !stopped {
		t.Error("second q must cancel the run")
	}
	if !m.dash.stopping {
		t.Error("second q must mark the run stopping")
	}
	if m.dash.stopArmed() {
		t.Error("confirming must disarm")
	}
}

// TestRunningStopGuardDisarms checks esc and any other key disarm the confirm
// without cancelling — mirroring the reset guard.
func TestRunningStopGuardDisarms(t *testing.T) {
	for _, disarm := range []tea.KeyPressMsg{keyPress("esc"), keyPress("j"), keyPress("x")} {
		stopped := false
		m := runningApp(120, 30, guardRows())
		m.loopCancel = func() { stopped = true }

		nm, _ := m.handleRunningKey(keyPress("q"))
		m = nm.(appModel)
		if !m.dash.stopArmed() {
			t.Fatalf("%q: first q must arm", disarm.String())
		}
		nm, _ = m.handleRunningKey(disarm)
		m = nm.(appModel)
		if stopped {
			t.Errorf("%q: disarming key must not cancel the run", disarm.String())
		}
		if m.dash.stopArmed() {
			t.Errorf("%q: must disarm the stop confirm", disarm.String())
		}
		if m.dash.stopping {
			t.Errorf("%q: must not mark the run stopping", disarm.String())
		}
	}
}

// TestRunningStopGuardCtrlCBypasses checks ctrl+c stays the unguarded emergency
// stop: it cancels immediately whether or not a q-confirm is armed.
func TestRunningStopGuardCtrlCBypasses(t *testing.T) {
	// Unarmed: ctrl+c cancels on the first press, no confirm.
	stopped := false
	m := runningApp(120, 30, guardRows())
	m.loopCancel = func() { stopped = true }
	nm, _ := m.handleRunningKey(keyCtrlC)
	m = nm.(appModel)
	if !stopped || !m.dash.stopping {
		t.Errorf("ctrl+c must cancel immediately (stopped=%v stopping=%v)", stopped, m.dash.stopping)
	}
	if m.dash.stopArmed() {
		t.Error("ctrl+c must not leave a stop armed")
	}

	// Armed: ctrl+c bypasses the pending confirm and still cancels now.
	stopped = false
	m2 := runningApp(120, 30, guardRows())
	m2.loopCancel = func() { stopped = true }
	nm, _ = m2.handleRunningKey(keyPress("q"))
	m2 = nm.(appModel)
	nm, _ = m2.handleRunningKey(keyCtrlC)
	m2 = nm.(appModel)
	if !stopped || !m2.dash.stopping || m2.dash.stopArmed() {
		t.Errorf("ctrl+c while armed must cancel and disarm (stopped=%v stopping=%v armed=%v)", stopped, m2.dash.stopping, m2.dash.stopArmed())
	}
}

// TestStandaloneStopGuardArmsThenConfirms is the same two-key stop for the direct
// `trau <args>` dashboard renderer (model.handleKey), which owns its own key path.
func TestStandaloneStopGuardArmsThenConfirms(t *testing.T) {
	interrupted := false
	d := freshDash(120, 30, "").withQueue(guardRows())
	d.onInterrupt = func() { interrupted = true }

	d, _, handled := d.handleKey(keyPress("q"))
	if !handled {
		t.Fatal("q must be handled")
	}
	if interrupted {
		t.Fatal("first q must not interrupt the run")
	}
	if !d.stopArmed() || d.stopping {
		t.Fatalf("first q must arm without stopping (armed=%v stopping=%v)", d.stopArmed(), d.stopping)
	}

	d, _, _ = d.handleKey(keyPress("q"))
	if !interrupted {
		t.Error("second q must interrupt the run")
	}
	if !d.stopping || d.stopArmed() {
		t.Errorf("second q must stop and disarm (stopping=%v armed=%v)", d.stopping, d.stopArmed())
	}
}

// TestStandaloneStopGuardEscDisarms checks esc disarms the standalone confirm, and
// that ctrl+c bypasses it — first press stops, second force-quits.
func TestStandaloneStopGuardEscDisarms(t *testing.T) {
	interrupted := false
	d := freshDash(120, 30, "").withQueue(guardRows())
	d.onInterrupt = func() { interrupted = true }

	d, _, _ = d.handleKey(keyPress("q"))
	if !d.stopArmed() {
		t.Fatal("first q must arm")
	}
	d, _, _ = d.handleKey(keyPress("esc"))
	if interrupted || d.stopArmed() || d.stopping {
		t.Errorf("esc must disarm without stopping (interrupted=%v armed=%v stopping=%v)", interrupted, d.stopArmed(), d.stopping)
	}

	// ctrl+c: first press stops (bypassing the guard), second force-quits.
	d, _, _ = d.handleKey(keyCtrlC)
	if !interrupted || !d.stopping {
		t.Errorf("ctrl+c must stop immediately (interrupted=%v stopping=%v)", interrupted, d.stopping)
	}
	_, cmd, _ := d.handleKey(keyCtrlC)
	if cmd == nil || !isQuitCmd(cmd) {
		t.Error("a second ctrl+c must force quit")
	}
}

// TestStopBannerCopyIsHonest checks the stop copy states what actually happens —
// the phase is interrupted, not finished — and that progress survives.
func TestStopBannerCopyIsHonest(t *testing.T) {
	d := freshDash(120, 30, "").markStopping()
	if got := d.banner; !strings.Contains(got, "interrupts") || !strings.Contains(got, "resumable") {
		t.Errorf("stop banner must say the phase is interrupted and resumable, got %q", got)
	}
	if strings.Contains(d.banner, "after this phase") {
		t.Errorf("stop banner must not claim the phase finishes, got %q", d.banner)
	}
}

func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// --- Run loop: whole-ready-queue confirm ---

func newLoopSetup(t *testing.T) loopSetupModel {
	t.Helper()
	return newLoopSetupModel(context.Background(), &fakeAppActions{}, DefaultStyles(), MenuInfo{Prefix: "COD"}, 80, 24)
}

// TestRunLoopBlankEnterArmsAndConfirms pins the biggest-blast-radius guard: a
// blank-input enter arms; a second enter starts the whole ready queue (blank epic).
func TestRunLoopBlankEnterArmsAndConfirms(t *testing.T) {
	ls := newLoopSetup(t)

	ls, _ = ls.handleKey(keyEnter)
	if ls.Done() {
		t.Fatal("first blank enter must not finish the screen")
	}
	if !ls.loopArmed {
		t.Fatal("first blank enter must arm the whole-queue confirm")
	}

	ls2, _ := ls.handleKey(keyEnter)
	if !ls2.Done() || ls2.Cancelled() || ls2.Epic() != "" {
		t.Errorf("second enter must start the whole queue (done=%v cancelled=%v epic=%q)", ls2.Done(), ls2.Cancelled(), ls2.Epic())
	}
}

// TestRunLoopArmedDisarms checks esc backs out from the armed confirm and typing
// disarms without starting — the whole-queue start never fires from a stray key.
func TestRunLoopArmedDisarms(t *testing.T) {
	armed := func(t *testing.T) loopSetupModel {
		t.Helper()
		ls, _ := newLoopSetup(t).handleKey(keyEnter)
		if !ls.loopArmed {
			t.Fatal("setup: first blank enter must arm")
		}
		return ls
	}

	escaped, _ := armed(t).handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !escaped.Cancelled() {
		t.Error("esc from the armed confirm must back out")
	}

	typed, _ := armed(t).handleKey(keyPress("C"))
	if typed.loopArmed {
		t.Error("typing must disarm the confirm")
	}
	if typed.Done() {
		t.Error("typing must not start the whole queue")
	}
	if typed.input.Value() != "C" {
		t.Errorf("the disarming character must reach the input, got %q", typed.input.Value())
	}
}

// TestRunLoopEpicEnterUnguarded checks the epic path is untouched: a non-blank enter
// loads the sub-issue preview (its own confirmation) rather than arming.
func TestRunLoopEpicEnterUnguarded(t *testing.T) {
	ls := newLoopSetup(t)
	ls.input.SetValue("COD-7")

	ls2, cmd := ls.handleKey(keyEnter)
	if ls2.loopArmed {
		t.Error("a non-blank enter must not arm the whole-queue confirm")
	}
	if ls2.step != loopLoading {
		t.Errorf("a non-blank enter must load the epic preview, step=%v", ls2.step)
	}
	if cmd == nil {
		t.Error("epic enter must kick off the sub-issue load")
	}
}

// TestRunLoopBlankEnterStartsRunAfterConfirm is the app-shell integration: from the
// menu, Run loop → blank enter (arm) → enter (start) lands on the running view.
func TestRunLoopBlankEnterStartsRunAfterConfirm(t *testing.T) {
	fake := &fakeAppActions{fakeOnboardActions: fakeOnboardActions{repoRoot: t.TempDir()}}
	m := newAppModel(context.Background(), fake, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = nm.(appModel)
	sm, _ := m.selectAction(actRun)
	m = sm.(appModel)

	nm, _ = m.Update(keyEnter)
	m = nm.(appModel)
	if m.view != viewRunLoop {
		t.Fatalf("first blank enter must stay on Run loop (armed), view=%d", m.view)
	}
	if !m.loopSetup.loopArmed {
		t.Fatal("first blank enter must arm the confirm")
	}

	nm, _ = m.Update(keyEnter)
	m = nm.(appModel)
	if m.view != viewRunning {
		t.Errorf("second enter must start the whole ready queue, view=%d", m.view)
	}
	if m.loopCancel != nil {
		m.loopCancel()
	}
}

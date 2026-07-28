import { useEffect, useRef, useState } from "react";

import { suggestGrillMode, type GrillMode } from "@/lib/grill";

// suggestDebounceMs is how long typing has to settle before the note is worth
// classifying. Losing focus asks at once, so this only paces a user still typing.
const suggestDebounceMs = 600;

// ModeSuggestion is a start surface's session type: the mode its chip shows, whether
// that value came from the hub rather than the user, and the acts a surface runs.
export interface ModeSuggestion {
  mode: GrillMode;
  suggested: boolean;
  choose: (mode: GrillMode) => void;
  noteChanged: (text: string) => void;
  noteSettled: (text: string) => void;
}

// useModeSuggestion pre-selects the session type from the focus note. The suggestion
// is advisory in both directions: it never starts anything, and it stops for good the
// moment the user flips the chip themselves — including for a response still in
// flight, so a slow classifier can never undo a deliberate choice.
export function useModeSuggestion(repo: string): ModeSuggestion {
  const [mode, setMode] = useState<GrillMode>("interview");
  const [suggested, setSuggested] = useState(false);
  const chosen = useRef(false);
  const asked = useRef("");
  const inflight = useRef(false);
  const queued = useRef<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const era = useRef(0);

  const cancel = () => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
  };
  useEffect(() => cancel, []);

  // forget withdraws the standing suggestion — the chip, the hint, and any reply still
  // in flight, which comes back on a spent era and is discarded. A mode the user picked
  // is not a reading of a note, so it stands.
  const forget = () => {
    cancel();
    era.current += 1;
    asked.current = "";
    inflight.current = false;
    queued.current = null;
    setSuggested(false);
    if (!chosen.current) setMode("interview");
  };

  // A suggestion reads one repo's note, so switching scope drops it rather than
  // carrying another repo's reading into this one.
  useEffect(() => {
    forget();
  }, [repo]);

  // ask keeps one request in flight and holds the latest note behind it, so a burst of
  // typing costs one classification plus at most one catch-up. Every call spawns an
  // agent, so a note unchanged since it was last sent is not re-sent; a note deleted
  // outright takes its suggestion with it.
  const ask = (text: string) => {
    if (chosen.current) return;
    if (text === "") {
      forget();
      return;
    }
    if (text === asked.current) return;
    if (inflight.current) {
      queued.current = text;
      return;
    }
    asked.current = text;
    inflight.current = true;
    const sent = era.current;
    void suggestGrillMode(repo, text).then((next) => {
      if (sent !== era.current) return;
      inflight.current = false;
      if (next && !chosen.current) {
        setMode(next);
        setSuggested(true);
      }
      const pending = queued.current;
      queued.current = null;
      if (pending !== null) ask(pending);
    });
  };

  return {
    mode,
    suggested,
    choose: (next) => {
      chosen.current = true;
      cancel();
      setSuggested(false);
      setMode(next);
    },
    noteChanged: (text) => {
      cancel();
      if (chosen.current) return;
      timer.current = setTimeout(() => ask(text.trim()), suggestDebounceMs);
    },
    noteSettled: (text) => {
      cancel();
      ask(text.trim());
    },
  };
}

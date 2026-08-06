import { useSyncExternalStore } from "react";

type Store = Pick<Storage, "getItem" | "setItem">;

// An install belongs to one browser, and so does the choice never to be offered
// one again: the mark is view state the hub has no use for, the same reasoning
// that keeps the sync-degraded dismissal in localStorage.
const DISMISSED_KEY = "trau.pwa-install.dismissed";
const DISMISSED_MARK = "1";

// The event Chromium fires when the app meets its install criteria and is not
// installed yet. No DOM lib declares it.
export interface BeforeInstallPromptEvent {
  preventDefault(): void;
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

export interface PwaInstall {
  available: boolean;
  dismissed: boolean;
  install: () => Promise<void>;
  dismiss: () => void;
}

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

// A browser that blocks storage reads as never dismissed, which costs the user
// one banner per page load rather than an exception on every route.
export function loadDismissed(): boolean {
  return Boolean(browserStore()?.getItem(DISMISSED_KEY));
}

const listeners = new Set<() => void>();
let dismissed: boolean | null = null;
let offer: BeforeInstallPromptEvent | null = null;

function currentDismissed(): boolean {
  if (dismissed === null) dismissed = loadDismissed();
  return dismissed;
}

export function installAvailable(): boolean {
  return offer !== null && !currentDismissed();
}

function publish(): void {
  listeners.forEach((notify) => notify());
}

// Dropping the cached mark sends the next read to storage — which is what
// another tab's dismissal leaves behind.
function onStorage(event: StorageEvent): void {
  if (event.key !== null && event.key !== DISMISSED_KEY) return;
  dismissed = null;
  publish();
}

function subscribe(notify: () => void): () => void {
  if (listeners.size === 0) globalThis.addEventListener("storage", onStorage);
  listeners.add(notify);
  return () => {
    listeners.delete(notify);
    if (listeners.size === 0) {
      globalThis.removeEventListener("storage", onStorage);
    }
  };
}

// The offer prompts once and cannot be revived, so spending it is what hides the
// banner after the native dialog closes and after an install done through the
// browser's own menu.
function spend(): void {
  if (offer === null) return;
  offer = null;
  publish();
}

// install must run inside the click handler: Chromium refuses a prompt that no
// user gesture asked for. A declined dialog spends the offer like an accepted
// one, but records nothing — a later beforeinstallprompt offers again.
export async function install(): Promise<void> {
  const offered = offer;
  if (offered === null) return;
  spend();
  try {
    await offered.prompt();
    await offered.userChoice;
  } catch {
    // The offer is spent whichever way the dialog ended.
  }
}

// dismiss is permanent for this browser. Where storage is unavailable the mark
// cannot outlive the page load, and the banner returns on the next visit.
export function dismiss(): void {
  browserStore()?.setItem(DISMISSED_KEY, DISMISSED_MARK);
  dismissed = true;
  publish();
}

function onBeforeInstallPrompt(event: Event): void {
  // Suppresses Chrome's own mini-infobar on Android; the banner speaks instead.
  event.preventDefault();
  offer = event as unknown as BeforeInstallPromptEvent;
  publish();
}

// Registered as the module loads, before React mounts, so an event the browser
// fires early is still caught. The guard keeps the module loadable off a browser.
if (typeof globalThis.addEventListener === "function") {
  globalThis.addEventListener("beforeinstallprompt", onBeforeInstallPrompt);
  globalThis.addEventListener("appinstalled", spend);
}

export function usePwaInstall(): PwaInstall {
  return {
    available: useSyncExternalStore(subscribe, installAvailable),
    dismissed: useSyncExternalStore(subscribe, currentDismissed),
    install,
    dismiss,
  };
}

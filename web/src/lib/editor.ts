import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Which editor is installed is a fact about the machine the browser runs on, not
// about the repo, so the choice lives in localStorage rather than in settings.
const EDITOR_KEY = 'trau.editor'

export type EditorID = 'vscode' | 'cursor' | 'windsurf' | 'zed' | 'jetbrains'

export const EDITORS: readonly { id: EditorID; label: string }[] = [
  { id: 'vscode', label: 'VS Code' },
  { id: 'cursor', label: 'Cursor' },
  { id: 'windsurf', label: 'Windsurf' },
  { id: 'zed', label: 'Zed' },
  { id: 'jetbrains', label: 'JetBrains' },
]

const DEFAULT_EDITOR: EditorID = 'vscode'

export function editorLabel(editor: EditorID): string {
  return EDITORS.find((e) => e.id === editor)?.label ?? editor
}

// editorUrl builds the deep link the browser hands to the OS protocol handler.
// The VS Code family carries the path as the URL path, so each segment is encoded
// on its own to keep the separators; JetBrains Toolbox takes it as a query value.
export function editorUrl(
  editor: EditorID,
  absPath: string,
  line?: number,
): string {
  if (editor === 'jetbrains') {
    const url = `idea://open?file=${encodeURIComponent(absPath)}`
    return line === undefined ? url : `${url}&line=${line}`
  }
  const path = absPath.split('/').map(encodeURIComponent).join('/')
  return `${editor}://file${path}${line === undefined ? '' : `:${line}`}`
}

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

function isEditorID(value: string | null): value is EditorID {
  return EDITORS.some((e) => e.id === value)
}

export function loadEditor(): EditorID {
  const stored = browserStore()?.getItem(EDITOR_KEY) ?? null
  return isEditorID(stored) ? stored : DEFAULT_EDITOR
}

export function storeEditor(editor: EditorID): void {
  browserStore()?.setItem(EDITOR_KEY, editor)
  publish(editor)
}

const listeners = new Set<() => void>()
let snapshot: EditorID | null = null

function currentEditor(): EditorID {
  if (snapshot === null) snapshot = loadEditor()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is
// what another tab's write leaves behind.
function publish(editor: EditorID | null): void {
  snapshot = editor
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === EDITOR_KEY) publish(null)
}

function subscribe(notify: () => void): () => void {
  if (listeners.size === 0) globalThis.addEventListener('storage', onStorage)
  listeners.add(notify)
  return () => {
    listeners.delete(notify)
    if (listeners.size === 0) {
      globalThis.removeEventListener('storage', onStorage)
    }
  }
}

// useEditor hands every link on the page the one chosen editor, so picking a
// different one in the header rewrites the per-file links with it.
export function useEditor(): [EditorID, (next: EditorID) => void] {
  return [useSyncExternalStore(subscribe, currentEditor), storeEditor]
}

import type { RunDiffFile } from './rundiff'

export interface DiffTreeFile {
  kind: 'file'
  path: string
  name: string
  file: RunDiffFile
}

export interface DiffTreeDir {
  kind: 'dir'
  path: string
  name: string
  children: DiffTreeNode[]
}

export type DiffTreeNode = DiffTreeDir | DiffTreeFile

interface DirBuilder {
  path: string
  dirs: Map<string, DirBuilder>
  files: DiffTreeFile[]
}

export function diffCardId(path: string): string {
  return `diff-file-${path}`
}

export function buildDiffTree(files: RunDiffFile[]): DiffTreeNode[] {
  const root: DirBuilder = { path: '', dirs: new Map(), files: [] }

  for (const file of files) {
    const cut = file.path.lastIndexOf('/')
    const segments = cut < 0 ? [] : file.path.slice(0, cut).split('/')
    let dir = root
    for (const segment of segments) dir = childDir(dir, segment)
    dir.files.push({
      kind: 'file',
      path: file.path,
      name: file.path.slice(cut + 1),
      file,
    })
  }

  return childNodes(root)
}

// filterDiffTree hands a blank query back the same tree, so memoized rows stay put.
export function filterDiffTree(
  nodes: DiffTreeNode[],
  query: string,
): DiffTreeNode[] {
  const needle = query.trim().toLowerCase()
  return needle === '' ? nodes : matching(nodes, needle)
}

function matching(nodes: DiffTreeNode[], needle: string): DiffTreeNode[] {
  const kept: DiffTreeNode[] = []
  for (const node of nodes) {
    if (node.kind === 'file') {
      if (node.path.toLowerCase().includes(needle)) kept.push(node)
      continue
    }
    const children = matching(node.children, needle)
    if (children.length > 0) kept.push({ ...node, children })
  }
  return kept
}

function childDir(parent: DirBuilder, segment: string): DirBuilder {
  const existing = parent.dirs.get(segment)
  if (existing) return existing
  const created: DirBuilder = {
    path: parent.path === '' ? segment : `${parent.path}/${segment}`,
    dirs: new Map(),
    files: [],
  }
  parent.dirs.set(segment, created)
  return created
}

function childNodes(dir: DirBuilder): DiffTreeNode[] {
  const dirs = [...dir.dirs.values()]
    .map(compact)
    .sort((a, b) => a.name.localeCompare(b.name))
  const files = [...dir.files].sort((a, b) => a.name.localeCompare(b.name))
  return [...dirs, ...files]
}

// compact folds a chain of single-child directories into one row (a/b/c) the way
// GitHub does, and keys it by the deepest path so expansion survives a refetch.
function compact(dir: DirBuilder): DiffTreeDir {
  let name = baseName(dir.path)
  let node = dir
  while (node.files.length === 0 && node.dirs.size === 1) {
    const [only] = node.dirs.values()
    name = `${name}/${baseName(only.path)}`
    node = only
  }
  return { kind: 'dir', path: node.path, name, children: childNodes(node) }
}

function baseName(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

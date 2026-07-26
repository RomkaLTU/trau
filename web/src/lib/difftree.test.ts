import { describe, expect, it } from 'vitest'

import { buildDiffTree, filterDiffTree, type DiffTreeNode } from './difftree'
import type { RunDiffFile } from './rundiff'

function file(path: string, over: Partial<RunDiffFile> = {}): RunDiffFile {
  return {
    path,
    status: 'modified',
    additions: 1,
    deletions: 0,
    binary: false,
    patch: '@@ -1 +1 @@',
    ...over,
  }
}

// rows flattens the tree the way it renders: one line per node, indented by depth,
// directories carrying their compacted label.
function rows(nodes: DiffTreeNode[], depth = 0): string[] {
  return nodes.flatMap((node) => {
    const line = `${'  '.repeat(depth)}${node.name}${node.kind === 'dir' ? '/' : ''}`
    return node.kind === 'dir' ? [line, ...rows(node.children, depth + 1)] : [line]
  })
}

describe('buildDiffTree', () => {
  it('nests files under their directories and lists directories before files', () => {
    const tree = buildDiffTree([
      file('README.md'),
      file('web/src/app.tsx'),
      file('web/package.json'),
    ])

    expect(rows(tree)).toEqual([
      'web/',
      '  src/',
      '    app.tsx',
      '  package.json',
      'README.md',
    ])
  })

  it('compacts a chain of single-child directories into one row', () => {
    const tree = buildDiffTree([file('internal/webserver/dist/assets/index.js')])

    expect(rows(tree)).toEqual([
      'internal/webserver/dist/assets/',
      '  index.js',
    ])
  })

  it('keys a compacted directory by its deepest path', () => {
    const [dir] = buildDiffTree([file('a/b/c/x.go')])

    expect(dir).toMatchObject({ kind: 'dir', path: 'a/b/c', name: 'a/b/c' })
  })

  it('stops compacting where a directory branches or holds a file of its own', () => {
    const tree = buildDiffTree([
      file('a/b/c/x.go'),
      file('a/b/d/y.go'),
      file('e/f.go'),
      file('e/g/h.go'),
    ])

    expect(rows(tree)).toEqual([
      'a/b/',
      '  c/',
      '    x.go',
      '  d/',
      '    y.go',
      'e/',
      '  g/',
      '    h.go',
      '  f.go',
    ])
  })

  it('sorts directories and files by name', () => {
    const tree = buildDiffTree([
      file('src/z.ts'),
      file('src/a.ts'),
      file('zeta/one.ts'),
      file('alpha/one.ts'),
    ])

    expect(rows(tree)).toEqual([
      'alpha/',
      '  one.ts',
      'src/',
      '  a.ts',
      '  z.ts',
      'zeta/',
      '  one.ts',
    ])
  })

  it('files a rename under its new path and carries the whole file through', () => {
    const renamed = file('web/src/lib/difftree.ts', {
      old_path: 'web/src/difftree.ts',
      status: 'renamed',
      additions: 12,
      deletions: 3,
    })
    const [dir] = buildDiffTree([renamed])

    expect(dir).toMatchObject({ path: 'web/src/lib' })
    expect(dir.kind === 'dir' && dir.children[0]).toEqual({
      kind: 'file',
      path: 'web/src/lib/difftree.ts',
      name: 'difftree.ts',
      file: renamed,
    })
  })

  it('keeps binary and patch-omitted files in the tree', () => {
    const tree = buildDiffTree([
      file('assets/logo.png', { binary: true, patch: '' }),
      file('assets/huge.json', { patch: '' }),
    ])

    expect(rows(tree)).toEqual(['assets/', '  huge.json', '  logo.png'])
  })
})

describe('filterDiffTree', () => {
  const tree = buildDiffTree([
    file('web/src/lib/rundiff.ts'),
    file('web/src/components/trau/run-diff.tsx'),
    file('internal/webserver/diff.go'),
  ])

  it('returns the tree untouched when the query is blank', () => {
    expect(filterDiffTree(tree, '   ')).toBe(tree)
  })

  it('keeps matching files and the folders leading to them', () => {
    expect(rows(filterDiffTree(tree, 'rundiff'))).toEqual([
      'web/src/',
      '  lib/',
      '    rundiff.ts',
    ])
  })

  it('matches case-insensitively on the full path', () => {
    expect(rows(filterDiffTree(tree, 'WEBSERVER'))).toEqual([
      'internal/webserver/',
      '  diff.go',
    ])
  })

  it('matches every file under a directory whose name matches', () => {
    expect(rows(filterDiffTree(tree, 'web/src'))).toEqual([
      'web/src/',
      '  components/trau/',
      '    run-diff.tsx',
      '  lib/',
      '    rundiff.ts',
    ])
  })

  it('drops the whole tree when nothing matches', () => {
    expect(filterDiffTree(tree, 'nothing-here')).toEqual([])
  })
})

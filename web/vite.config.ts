/// <reference types="vitest/config" />
import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'

const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf8'),
) as { version: string }

function gitBuild(): string {
  try {
    return 'g' + execSync('git rev-parse --short HEAD').toString().trim()
  } catch {
    return 'gunknown'
  }
}

const build = gitBuild()

// Emitted rather than bundled as an input: a Rollup input lands under assets/
// as an ES module, which would need `{ type: 'module' }` registration.
function serviceWorker(): Plugin {
  const source = fileURLToPath(new URL('./src/sw.js', import.meta.url))
  return {
    name: 'trau-service-worker',
    apply: 'build',
    generateBundle(_options, bundle) {
      const shell = Object.keys(bundle)
        .filter((name) => name.endsWith('.js') || name.endsWith('.css'))
        .map((name) => `/${name}`)
      this.emitFile({
        type: 'asset',
        fileName: 'sw.js',
        source: readFileSync(source, 'utf8')
          .replaceAll('__WEB_BUILD__', build)
          .replaceAll('__WEB_SHELL__', JSON.stringify(['/', ...shell])),
      })
    },
  }
}

export default defineConfig({
  define: {
    __WEB_VERSION__: JSON.stringify(pkg.version),
    __WEB_BUILD__: JSON.stringify(build),
  },
  plugins: [
    tanstackRouter({ target: 'react', autoCodeSplitting: false }),
    react(),
    tailwindcss(),
    serviceWorker(),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  base: '/',
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
  build: {
    outDir: '../internal/webserver/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name].js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name][extname]',
      },
    },
  },
})

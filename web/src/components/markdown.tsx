import { createContext, useContext, useState, type ReactNode } from "react";
import { FileImage } from "lucide-react";

import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// MarkdownUrlMap rewrites the tracker URLs an issue body carries to the hub
// attachment that cached their bytes. A tracker-hosted image it does not cover
// cannot be loaded at all — those bytes sit behind credentials the browser does
// not hold — so it renders as a placeholder rather than a broken image.
export type MarkdownUrlMap = Record<string, string>;

const EMPTY_URL_MAP: MarkdownUrlMap = {};

const UrlMapContext = createContext<MarkdownUrlMap>(EMPTY_URL_MAP);

type Block =
  | { kind: "heading"; level: number; text: string }
  | { kind: "code"; text: string }
  | { kind: "list"; ordered: boolean; items: string[] }
  | { kind: "table"; header: string[]; rows: string[][] }
  | { kind: "quote"; blocks: Block[] }
  | { kind: "rule" }
  | { kind: "paragraph"; text: string };

const HEADING = /^(#{1,6})\s+(.*)$/;
const BULLET = /^\s*[-*]\s+/;
const ORDERED = /^\s*\d+\.\s+/;
const QUOTE = /^\s*>\s?/;
const RULE = /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/;
const TABLE_ROW = /^\s*\|.*\|\s*$/;
const TABLE_DELIM = /^\s*\|(?:\s*:?-+:?\s*\|)+\s*$/;

function isTableStart(lines: string[], i: number): boolean {
  return (
    TABLE_ROW.test(lines[i]) &&
    i + 1 < lines.length &&
    TABLE_DELIM.test(lines[i + 1])
  );
}

function isBlockStart(lines: string[], i: number): boolean {
  const line = lines[i];
  return (
    HEADING.test(line) ||
    BULLET.test(line) ||
    ORDERED.test(line) ||
    QUOTE.test(line) ||
    RULE.test(line) ||
    line.trimStart().startsWith("```") ||
    isTableStart(lines, i)
  );
}

function splitRow(line: string): string[] {
  return line
    .trim()
    .slice(1, -1)
    .split("|")
    .map((cell) => cell.trim());
}

export function parseBlocks(md: string): Block[] {
  const lines = md.replace(/\r\n/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (line.trim() === "") {
      i++;
      continue;
    }

    if (line.trimStart().startsWith("```")) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith("```")) {
        body.push(lines[i]);
        i++;
      }
      i++;
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }

    if (RULE.test(line)) {
      blocks.push({ kind: "rule" });
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const body: string[] = [];
      while (i < lines.length && QUOTE.test(lines[i])) {
        body.push(lines[i].replace(QUOTE, ""));
        i++;
      }
      blocks.push({ kind: "quote", blocks: parseBlocks(body.join("\n")) });
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({
        kind: "heading",
        level: heading[1].length,
        text: heading[2].trim(),
      });
      i++;
      continue;
    }

    if (BULLET.test(line) || ORDERED.test(line)) {
      const ordered = ORDERED.test(line);
      const marker = ordered ? ORDERED : BULLET;
      const items: string[] = [];
      while (i < lines.length && marker.test(lines[i])) {
        items.push(lines[i].replace(marker, ""));
        i++;
      }
      blocks.push({ kind: "list", ordered, items });
      continue;
    }

    if (isTableStart(lines, i)) {
      const header = splitRow(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && TABLE_ROW.test(lines[i])) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      blocks.push({ kind: "table", header, rows });
      continue;
    }

    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !isBlockStart(lines, i)
    ) {
      para.push(lines[i].trim());
      i++;
    }
    blocks.push({ kind: "paragraph", text: para.join(" ") });
  }

  return blocks;
}

const CODE = "`(?<code>[^`]+)`";
const BOLD_ITALIC = String.raw`\*\*\*(?<bolditalic>[^*]+)\*\*\*`;
const BOLD = String.raw`\*\*(?<bold>[^*]+)\*\*`;
const ITALIC = String.raw`\*(?<em>[^*\s][^*]*)\*`;
const IMAGE = String.raw`!\[(?<alt>[^\]]*)\]\(\s*<?(?<src>[^)>\s]+)>?(?:\s+"[^"]*")?\s*\)`;
const LINK = String.raw`\[(?<label>[^\]]*)\]\(\s*<?(?<href>[^)>\s]+)>?(?:\s+"[^"]*")?\s*\)`;

const INLINE = new RegExp(
  [CODE, BOLD_ITALIC, BOLD, IMAGE, LINK, ITALIC].join("|"),
  "g",
);
const IMAGE_REF = new RegExp(IMAGE, "g");

type InlineGroups = {
  code?: string;
  bolditalic?: string;
  bold?: string;
  em?: string;
  alt?: string;
  src?: string;
  label?: string;
  href?: string;
};

function groupsOf(m: RegExpExecArray | RegExpMatchArray): InlineGroups {
  return m.groups as InlineGroups;
}

// markdownImageSources lists the image URLs a body embeds, so a caller can tell
// which of an issue's attachments its markdown already displays.
export function markdownImageSources(md: string): string[] {
  const out: string[] = [];
  for (const m of md.matchAll(IMAGE_REF)) {
    const { src } = groupsOf(m);
    if (src) out.push(src);
  }
  return out;
}

function renderInline(text: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let last = 0;
  let key = 0;
  for (let m = INLINE.exec(text); m !== null; m = INLINE.exec(text)) {
    if (m.index > last) {
      nodes.push(text.slice(last, m.index));
    }
    const { code, bolditalic, bold, em, alt, src, label, href } = groupsOf(m);
    if (code !== undefined) {
      nodes.push(
        <code
          key={key++}
          className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]"
        >
          {code}
        </code>,
      );
    } else if (bolditalic !== undefined) {
      nodes.push(
        <strong key={key++} className="font-semibold text-foreground">
          <em>{bolditalic}</em>
        </strong>,
      );
    } else if (bold !== undefined) {
      nodes.push(
        <strong key={key++} className="font-semibold text-foreground">
          {bold}
        </strong>,
      );
    } else if (em !== undefined) {
      nodes.push(<em key={key++}>{em}</em>);
    } else if (src !== undefined) {
      nodes.push(<InlineImage key={key++} src={src} alt={alt ?? ""} />);
    } else if (href !== undefined) {
      nodes.push(<InlineLink key={key++} href={href} label={label ?? ""} />);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    nodes.push(text.slice(last));
  }
  return nodes;
}

// trackerAttachmentPath matches the two shapes a Jira file URL takes, so a repo
// on a custom Jira domain is recognised as well as one on atlassian.net.
const trackerAttachmentPath = /\/(?:secure|rest\/api\/[^/]+)\/attachment\//;

function trackerHosted(url: URL): boolean {
  const host = url.hostname.toLowerCase();
  return (
    host === "uploads.linear.app" ||
    host.endsWith(".atlassian.net") ||
    trackerAttachmentPath.test(url.pathname)
  );
}

// displayableSrc resolves a markdown image URL to something the browser can
// actually load: the hub attachment it maps to, a same-origin hub path, or a
// public http(s) URL. Anything else yields null and renders as a placeholder.
function displayableSrc(raw: string, urlMap: MarkdownUrlMap): string | null {
  const mapped = urlMap[raw];
  if (mapped) return mapped;
  if (raw.startsWith("/")) return raw;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  return trackerHosted(url) ? null : raw;
}

function fileNameOf(src: string): string {
  const path = src.split(/[?#]/)[0];
  return path.slice(path.lastIndexOf("/") + 1);
}

function InlineImage({ src, alt }: { src: string; alt: string }) {
  const urlMap = useContext(UrlMapContext);
  const [broken, setBroken] = useState(false);
  const resolved = displayableSrc(src, urlMap);
  const label = alt || fileNameOf(src);

  if (resolved === null || broken) {
    return (
      <span className="mt-2 inline-flex max-w-full items-center gap-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        <FileImage className="size-3.5 shrink-0" aria-hidden />
        <span className="truncate">{label || "image"}</span>
      </span>
    );
  }

  return (
    <Dialog>
      <DialogTrigger asChild>
        <button type="button" className="mt-2 block cursor-zoom-in">
          <img
            src={resolved}
            alt={alt}
            loading="lazy"
            onError={() => setBroken(true)}
            className="max-h-80 max-w-full rounded-md border"
          />
        </button>
      </DialogTrigger>
      <DialogContent
        aria-describedby={undefined}
        className="max-w-[92vw] p-2 sm:max-w-4xl"
      >
        <DialogTitle className="sr-only">{label || "Image"}</DialogTitle>
        <img
          src={resolved}
          alt={alt}
          className="max-h-[80vh] w-full rounded object-contain"
        />
      </DialogContent>
    </Dialog>
  );
}

function InlineLink({ href, label }: { href: string; label: string }) {
  const external = /^https?:\/\//i.test(href);
  return (
    <a
      href={href}
      className="text-primary underline underline-offset-2 hover:no-underline"
      {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
    >
      {label || href}
    </a>
  );
}

const headingClass: Record<number, string> = {
  1: "text-lg font-semibold",
  2: "text-base font-semibold",
  3: "text-sm font-semibold",
};

function Block({ block }: { block: Block }) {
  switch (block.kind) {
    case "heading":
      return (
        <p
          className={cn(
            "mt-4 first:mt-0 text-foreground",
            headingClass[block.level] ?? headingClass[3],
          )}
        >
          {renderInline(block.text)}
        </p>
      );
    case "code":
      return (
        <pre className="mt-3 overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">
          <code>{block.text}</code>
        </pre>
      );
    case "list": {
      const Tag = block.ordered ? "ol" : "ul";
      return (
        <Tag
          className={cn(
            "mt-2 flex flex-col gap-1 pl-5",
            block.ordered ? "list-decimal" : "list-disc",
          )}
        >
          {block.items.map((item, i) => (
            <li key={i} className="pl-1">
              {renderInline(item)}
            </li>
          ))}
        </Tag>
      );
    }
    case "table":
      return (
        <div className="mt-3 overflow-x-auto">
          <table className="w-full border-collapse">
            <thead>
              <tr>
                {block.header.map((cell, i) => (
                  <th
                    key={i}
                    className="border border-border bg-muted px-2 py-1 text-left font-semibold text-foreground"
                  >
                    {renderInline(cell)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.map((cells, i) => (
                <tr key={i}>
                  {cells.map((cell, j) => (
                    <td
                      key={j}
                      className="border border-border px-2 py-1 align-top"
                    >
                      {renderInline(cell)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case "quote":
      return (
        <blockquote className="mt-2 border-l-2 border-border pl-3">
          {block.blocks.map((child, i) => (
            <Block key={i} block={child} />
          ))}
        </blockquote>
      );
    case "rule":
      return <hr className="mt-3 border-border" />;
    case "paragraph":
      return (
        <p className="mt-2 first:mt-0 leading-relaxed">
          {renderInline(block.text)}
        </p>
      );
  }
}

export function Markdown({
  children,
  className,
  urlMap = EMPTY_URL_MAP,
}: {
  children: string;
  className?: string;
  urlMap?: MarkdownUrlMap;
}) {
  const blocks = parseBlocks(children);
  return (
    <UrlMapContext.Provider value={urlMap}>
      <div className={cn("text-sm text-muted-foreground", className)}>
        {blocks.map((block, i) => (
          <Block key={i} block={block} />
        ))}
      </div>
    </UrlMapContext.Provider>
  );
}

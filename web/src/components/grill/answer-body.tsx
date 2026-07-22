import type { ReactNode } from "react";

import { InlineImage } from "@/components/markdown";

// HUB_IMAGE matches the markdown an image paste inserts. It is the only thing an
// answer is scanned for — the rest stays as typed, never read as markdown.
const HUB_IMAGE =
  /!\[([^\]]*)\]\(((?:\/api\/v1)?\/repos\/[^)\s]+\/attachments\/\d+)\)/g;

export function AnswerBody({ text }: { text: string }) {
  const nodes: ReactNode[] = [];
  let last = 0;
  let key = 0;
  for (let m = HUB_IMAGE.exec(text); m !== null; m = HUB_IMAGE.exec(text)) {
    if (m.index > last) {
      nodes.push(text.slice(last, m.index));
    }
    nodes.push(<InlineImage key={key++} src={m[2]} alt={m[1]} />);
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    nodes.push(text.slice(last));
  }
  return <>{nodes}</>;
}

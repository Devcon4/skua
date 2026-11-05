export type ClassifiedInput =
  {kind: 'ignore'} | {kind: 'text'; text: string; from?: string};

const LEADING_REMINDER = /^\s*<system-reminder>[\s\S]*?<\/system-reminder>\s*/;
const LEADING_NOTICE = /^<system-notice>[\s\S]*?<\/system-notice>/;
const IRC_OPEN = '<irc>';
const IRC_CLOSE = '</irc>';
const IRC_HEADER = /^Incoming IRC message from agent `([^`]+)`:$/;
const IRC_BODY_END =
  /^(?:Sent while waiting|Sent while working|If response expected)/;
const PARENT_IRC =
  /^(?:\[Wait interrupted by message\]\s*)?<irc from="[^"]*" agent="([^"]*)">\r?\n([\s\S]*?)\r?\n<\/irc>$/;

export function classifyInput(text: string): ClassifiedInput {
  let rest = text;
  for (
    let m = LEADING_REMINDER.exec(rest);
    m !== null;
    m = LEADING_REMINDER.exec(rest)
  ) {
    rest = rest.slice(m[0].length);
  }
  rest = rest.trim();
  if (rest === '') return {kind: 'ignore'};

  const notice = LEADING_NOTICE.exec(rest);
  if (notice !== null) rest = rest.slice(notice[0].length).trim();

  const [, parentAgent, parentBody] = PARENT_IRC.exec(rest) ?? [];
  if (parentBody !== undefined)
    return textOrIgnore(parentBody.trim(), parentAgent);
  if (notice !== null) return textOrIgnore(rest);

  if (rest.startsWith(IRC_OPEN) && rest.endsWith(IRC_CLOSE)) {
    return parseIrc(
      rest.slice(IRC_OPEN.length, rest.length - IRC_CLOSE.length),
    );
  }

  return {kind: 'text', text: rest};
}

function parseIrc(inner: string): ClassifiedInput {
  const lines = inner.trim().split(/\r?\n/);
  const header = IRC_HEADER.exec(lines[0] ?? '');
  if (header === null || lines[1]?.trim() !== '')
    return textOrIgnore(inner.trim());

  const bodyLines = lines.slice(2);
  const end = bodyLines.findIndex(line => IRC_BODY_END.test(line));
  const body = bodyLines
    .slice(0, end === -1 ? undefined : end)
    .join('\n')
    .trim();
  return textOrIgnore(body, header[1]);
}

function textOrIgnore(text: string, from?: string): ClassifiedInput {
  if (text === '') return {kind: 'ignore'};
  return from === undefined ? {kind: 'text', text} : {kind: 'text', text, from};
}

export function extractBatchContext(
  systemPrompt: string | undefined,
): string | undefined {
  if (systemPrompt === undefined) return undefined;
  const lines = systemPrompt.split(/\r?\n/);
  const start = lines.indexOf('§ Context');
  if (start === -1) return undefined;

  const section = lines.slice(start + 1);
  const end = section.findIndex(line => line.startsWith('§ '));
  const context = section
    .slice(0, end === -1 ? undefined : end)
    .join('\n')
    .trim();
  return context === '' ? undefined : context;
}

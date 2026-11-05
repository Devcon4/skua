import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { classifyInput, extractBatchContext } from "../../src/inputs.ts";

const dateReminder =
  "<system-reminder>\nToday: 2026-11-03; current working directory: '/tmp/skua-scratch'. Do not repeat this information in your reply.\n</system-reminder>";
const idleReminder =
  "<system-reminder>\nLast turn had no tool call → session idle. Reminder 1 of 3.\nCall a tool or yield.\n</system-reminder>";
const hubNotice =
  "<system-notice>\nUser interjection during work: priority; supersedes conflicting prior instructions. Re-read; ensure current work reflects user intent.\n</system-notice>";
const ircHeader = "Incoming IRC message from agent `Main`:";
const ircHint = "If response expected, reply via `write` (path: agent://Main), when done.";

describe("classifyInput", () => {
  test("assignment keeps the text after the date reminder", () => {
    const text = `${dateReminder}\nComplete assignment thoroughly:\n\nList the files in pkg1.`;
    assert.deepEqual(classifyInput("user", text), {
      kind: "text",
      text: "Complete assignment thoroughly:\n\nList the files in pkg1.",
    });
  });

  test("two stacked leading reminders before an assignment are both removed", () => {
    const text = `${dateReminder}\n${idleReminder}\n\nComplete assignment thoroughly:\n\nList pkg2.`;
    assert.deepEqual(classifyInput("user", text), {
      kind: "text",
      text: "Complete assignment thoroughly:\n\nList pkg2.",
    });
  });

  test("reminder-only developer message is ignored", () => {
    assert.deepEqual(classifyInput("developer", idleReminder), { kind: "ignore" });
    assert.deepEqual(classifyInput("developer", `\n${idleReminder}\n\n${dateReminder}\n`), { kind: "ignore" });
  });

  test("Agent Hub steer drops the notice block", () => {
    const text = `${hubNotice}\nSTEER-A: focus on pkg3 first`;
    assert.deepEqual(classifyInput("user", text), { kind: "text", text: "STEER-A: focus on pkg3 first" });
  });

  test("notice with an empty body is ignored", () => {
    assert.deepEqual(classifyInput("user", `${hubNotice}\n  \n`), { kind: "ignore" });
  });

  test("parent IRC message yields the body and the sender", () => {
    const text = `<irc>\n${ircHeader}\n\nSTEER-M3: also check pkg9\n${ircHint}</irc>`;
    assert.deepEqual(classifyInput("developer", text), {
      kind: "text",
      text: "STEER-M3: also check pkg9",
      from: "Main",
    });
  });

  test("IRC message with the Sent while waiting paragraph stops before it", () => {
    const text = `<irc>\n${ircHeader}\n\nSTEER-M4: check pkg4\nthen pkg5\n\nSent while waiting.\n\n${ircHint}\n</irc>`;
    assert.deepEqual(classifyInput("developer", text), {
      kind: "text",
      text: "STEER-M4: check pkg4\nthen pkg5",
      from: "Main",
    });
  });

  test("IRC message with the Sent while working paragraph stops before it", () => {
    const text = `<irc>\n${ircHeader}\n\nSTEER-M7: skip pkg5\n\nSent while working.\n\n${ircHint}\n</irc>`;
    assert.deepEqual(classifyInput("developer", text), {
      kind: "text",
      text: "STEER-M7: skip pkg5",
      from: "Main",
    });
  });

  test("parent IRC steer to a working child yields the body and the sender", () => {
    const text = '[Wait interrupted by message]\n<irc from="parent" agent="Main">\nSTEER-M8: also check pkg7\nthen pkg8\n</irc>';
    assert.deepEqual(classifyInput("user", text), {
      kind: "text",
      text: "STEER-M8: also check pkg7\nthen pkg8",
      from: "Main",
    });
  });

  test("parent IRC steer tolerates CRLF, surrounding whitespace and a missing interrupt line", () => {
    const text = '\r\n  <irc from="parent" agent="Lead">\r\nSTEER-M9: skip pkg1\r\n</irc>  \r\n';
    assert.deepEqual(classifyInput("user", text), { kind: "text", text: "STEER-M9: skip pkg1", from: "Lead" });
  });

  test("parent IRC steer behind an interjection notice yields the body and the sender", () => {
    const text = `${hubNotice}\n[Wait interrupted by message]\n<irc from="parent" agent="Main">\nSTEER-M10: also check pkg7\n</irc>`;
    assert.deepEqual(classifyInput("user", text), { kind: "text", text: "STEER-M10: also check pkg7", from: "Main" });
  });

  test("malformed IRC envelope falls back to the inner text", () => {
    const text = "<irc>\nFrom Main: STEER-M5: check pkg6\n</irc>";
    assert.deepEqual(classifyInput("developer", text), { kind: "text", text: "From Main: STEER-M5: check pkg6" });
  });

  test("IRC header without the blank line falls back to the inner text", () => {
    const text = `<irc>\n${ircHeader}\nSTEER-M6: check pkg7\n</irc>`;
    assert.deepEqual(classifyInput("developer", text), {
      kind: "text",
      text: `${ircHeader}\nSTEER-M6: check pkg7`,
    });
  });

  test("plain text is trimmed", () => {
    assert.deepEqual(classifyInput("user", "  STEER-P: stop after pkg8 \n"), {
      kind: "text",
      text: "STEER-P: stop after pkg8",
    });
  });

  test("empty text is ignored", () => {
    assert.deepEqual(classifyInput("user", " \n "), { kind: "ignore" });
  });
});

describe("extractBatchContext", () => {
  test("returns the Context section up to the next section", () => {
    const prompt = [
      "§ Role",
      "Worker agent.",
      "§ Context",
      "",
      "Shared goal: audit pkg1 to pkg9.",
      "Report findings as a list.",
      "",
      "§ Coop",
      "Peers: Main.",
    ].join("\n");
    assert.equal(extractBatchContext(prompt), "Shared goal: audit pkg1 to pkg9.\nReport findings as a list.");
  });

  test("returns the Context section when it is the last section", () => {
    assert.equal(extractBatchContext("§ Role\nWorker.\n§ Context\nGoal: x.\n"), "Goal: x.");
  });

  test("returns undefined without a Context section", () => {
    assert.equal(extractBatchContext("§ Role\nWorker agent.\n§ Coop\nPeers: Main."), undefined);
  });

  test("returns undefined for an empty Context section", () => {
    assert.equal(extractBatchContext("§ Context\n\n§ Coop\nPeers: Main."), undefined);
  });

  test("returns undefined without a system prompt", () => {
    assert.equal(extractBatchContext(undefined), undefined);
  });
});

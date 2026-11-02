// SPDX-License-Identifier: AGPL-3.0-only
// CodeMirror 6 editor. Loaded lazily so it stays out of the main bundle.
import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { EditorView, basicSetup } from "codemirror";
import { Compartment, EditorState } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { indentWithTab } from "@codemirror/commands";
import { LanguageDescription, syntaxHighlighting, defaultHighlightStyle, HighlightStyle } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { oneDarkHighlightStyle } from "@codemirror/theme-one-dark";
import { tags as t } from "@lezer/highlight";

export interface EditorHandle {
  getValue: () => string;
  setValue: (v: string) => void;
  /** Marks the current text as saved. */
  markClean: () => void;
  focus: () => void;
}

// Light palette tuned to Wargo's "Northern" colours.
const lightHighlight = HighlightStyle.define([
  { tag: [t.keyword, t.modifier, t.operatorKeyword], color: "#8f3f8c" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "#1d7348" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "#a85e0a" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "#6b7683", fontStyle: "italic" },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: "#1f6c94" },
  { tag: [t.typeName, t.className, t.namespace], color: "#9a5b00" },
  { tag: [t.propertyName, t.attributeName], color: "#2a5f8f" },
  { tag: [t.heading], color: "#121820", fontWeight: "600" },
  { tag: [t.link, t.url], color: "#1f6c94", textDecoration: "underline" },
  { tag: [t.tagName], color: "#b03a2e" },
  { tag: t.invalid, color: "#c4382f" },
]);

const baseTheme = (dark: boolean) =>
  EditorView.theme(
    {
      "&": { height: "100%", backgroundColor: "var(--surface)", color: "var(--fg)", fontSize: "13px" },
      ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.55" },
      ".cm-content": { caretColor: "var(--accent)", padding: "8px 0" },
      ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--accent)", borderLeftWidth: "2px" },
      ".cm-gutters": { backgroundColor: "var(--surface-2)", color: "var(--fg-subtle)", borderRight: "1px solid var(--border)" },
      ".cm-activeLineGutter": { backgroundColor: "var(--surface-3)", color: "var(--fg)" },
      ".cm-activeLine": { backgroundColor: dark ? "rgb(255 255 255 / 0.03)" : "rgb(16 24 32 / 0.035)" },
      "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
        backgroundColor: "color-mix(in oklab, var(--accent) 28%, transparent) !important",
      },
      ".cm-selectionMatch": { backgroundColor: "color-mix(in oklab, var(--frost) 22%, transparent)" },
      "&.cm-focused": { outline: "none" },
      ".cm-panels": { backgroundColor: "var(--surface-2)", color: "var(--fg)", borderColor: "var(--border)" },
      ".cm-panels input, .cm-panels button": { fontFamily: "inherit" },
      ".cm-textfield": { backgroundColor: "var(--surface)", border: "1px solid var(--border)", borderRadius: "4px", color: "var(--fg)" },
      ".cm-button": { backgroundImage: "none", backgroundColor: "var(--surface-3)", border: "1px solid var(--border)", borderRadius: "4px", color: "var(--fg)" },
      ".cm-tooltip": { backgroundColor: "var(--surface)", border: "1px solid var(--border-strong)", color: "var(--fg)" },
      ".cm-foldPlaceholder": { backgroundColor: "var(--surface-3)", border: "none", color: "var(--fg-muted)" },
      ".cm-searchMatch": { backgroundColor: "color-mix(in oklab, var(--warn) 30%, transparent)" },
    },
    { dark },
  );

const highlight = (dark: boolean) => [syntaxHighlighting(dark ? oneDarkHighlightStyle : lightHighlight), syntaxHighlighting(defaultHighlightStyle, { fallback: true })];

const CodeEditor = forwardRef<
  EditorHandle,
  {
    initial: string;
    filename: string;
    dark: boolean;
    readOnly?: boolean;
    wrap?: boolean;
    onDirty: (dirty: boolean) => void;
    onSave: () => void;
  }
>(function CodeEditor({ initial, filename, dark, readOnly, wrap, onDirty, onSave }, ref) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const comps = useRef({ lang: new Compartment(), theme: new Compartment(), ro: new Compartment(), wrap: new Compartment() });
  const saveRef = useRef(onSave);
  saveRef.current = onSave;
  const dirtyRef = useRef(onDirty);
  dirtyRef.current = onDirty;
  const baseline = useRef(initial);

  useImperativeHandle(ref, () => ({
    getValue: () => view.current?.state.doc.toString() ?? "",
    setValue: (v: string) => {
      const ed = view.current;
      if (!ed) return;
      baseline.current = v;
      ed.dispatch({ changes: { from: 0, to: ed.state.doc.length, insert: v } });
      dirtyRef.current(false);
    },
    markClean: () => {
      baseline.current = view.current?.state.doc.toString() ?? "";
      dirtyRef.current(false);
    },
    focus: () => view.current?.focus(),
  }));

  useEffect(() => {
    const c = comps.current;
    const state = EditorState.create({
      doc: initial,
      extensions: [
        basicSetup,
        keymap.of([
          indentWithTab,
          {
            key: "Mod-s",
            preventDefault: true,
            run: () => {
              saveRef.current();
              return true;
            },
          },
        ]),
        c.lang.of([]),
        c.theme.of([baseTheme(dark), highlight(dark)]),
        c.ro.of(EditorState.readOnly.of(!!readOnly)),
        c.wrap.of(wrap ? EditorView.lineWrapping : []),
        EditorView.contentAttributes.of({ "aria-label": `Editor: ${filename}` }),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) {
            const d = u.state.doc;
            // Cheap length check first; compare text only when lengths match.
            dirtyRef.current(d.length !== baseline.current.length || d.toString() !== baseline.current);
          }
        }),
      ],
    });
    const v = new EditorView({ state, parent: host.current! });
    view.current = v;
    let alive = true;
    const desc = LanguageDescription.matchFilename(languages, filename);
    desc?.load().then((support) => {
      if (alive) v.dispatch({ effects: c.lang.reconfigure(support) });
    });
    return () => {
      alive = false;
      v.destroy();
      view.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    view.current?.dispatch({ effects: comps.current.theme.reconfigure([baseTheme(dark), highlight(dark)]) });
  }, [dark]);
  useEffect(() => {
    view.current?.dispatch({ effects: comps.current.ro.reconfigure(EditorState.readOnly.of(!!readOnly)) });
  }, [readOnly]);
  useEffect(() => {
    view.current?.dispatch({ effects: comps.current.wrap.reconfigure(wrap ? EditorView.lineWrapping : []) });
  }, [wrap]);

  return <div ref={host} className="h-full min-h-0 overflow-hidden" />;
});

export default CodeEditor;

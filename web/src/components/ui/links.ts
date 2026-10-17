/** A link that sits among other text (a sentence, a list of findings): the
 * text keeps the body colour and the underline carries the accent, so the
 * link never depends on colour alone. Standalone links can be `text-accent`
 * (the accent token meets 4.5:1 on every surface, in both themes). */
export const plainLink = "text-fg underline decoration-accent underline-offset-2 hover:text-accent";

/** A standalone text link a finger should hit on a touch screen (an
 * inspector's "View host…"): 44 px tall there, unchanged with a mouse. */
export const touchLink = "pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";

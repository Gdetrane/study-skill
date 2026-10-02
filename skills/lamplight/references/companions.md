# Companions

Companions are other skills and tools that make Lessons richer. Lamplight never needs
them: suggest one only when it is installed in your agent, and teach well without it.
Look at the skills and tools you have; ask the learner when unsure.

## Maths and physics: KaTeX

Write formulas in Lesson notes as LaTeX: `$...$` inline, `$$...$$` on their own line. For
an HTML page, load KaTeX's auto-render:

```html
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/katex@0.16/dist/katex.min.css">
<script defer src="https://cdn.jsdelivr.net/npm/katex@0.16/dist/katex.min.js"></script>
<script defer src="https://cdn.jsdelivr.net/npm/katex@0.16/dist/contrib/auto-render.min.js"
  onload="renderMathInElement(document.body);"></script>
```

## Visuals

With a visual-explainer skill, ask it for an HTML page and name the library:

| Content | Library |
|---|---|
| Equations | KaTeX |
| 2D function plots, geometry | JSXGraph |
| 3D surfaces | Plotly.js |
| Physics simulations | p5.js with Matter.js |
| Diagrams and flows | Mermaid |
| Circuits, molecules, star charts | an SVG generator (SchemDraw, RDKit, Starplot) |

Save visuals under `notes/visuals/`. Without one, draw an ASCII diagram in the Lesson.

## Live documentation

With a live-docs tool such as context7, check current APIs before writing a Lesson about a
library or framework, so its examples and pitfalls are up to date.

## Domain packs

With a domain pack, such as SciAgent-Skills for scientific Topics, use its workflows and
parameter tables when you write Lessons, and its recipes as private checks for feedback,
kept in Teacher's notes.

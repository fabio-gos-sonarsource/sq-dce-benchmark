# Bundled sample seeds (pre-scanned)

These are **pre-generated SonarScanner reports** replayed by the benchmark when
`seed_repo` is empty — so a sample run needs no scanner, JDK/Maven, or network build.

| Sample (`sample:`) | Project | Licence | ~ncloc |
|---|---|---|---|
| `js` | [React](https://github.com/facebook/react) | MIT (`react.LICENSE`) | ~98k |
| `java` | [jackson-databind](https://github.com/FasterXML/jackson-databind) | Apache-2.0 (`jackson.LICENSE`) | ~76k |

`*.zip` hold each project's `scanner-report/` (which embeds the project source), generated
against SonarQube 2026.1. Regenerate by scanning the project with `sonar.scanner.keepReport=true`
(the `analysis-cache2.pb` / `analysis-warnings.pb` entries are excluded — not needed for replay).

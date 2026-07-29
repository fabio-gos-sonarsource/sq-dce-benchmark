# Bundled sample seeds (pre-scanned)

These are **pre-generated SonarScanner reports** replayed by the benchmark when
`seed_repo` is empty — so a sample run needs no scanner, JDK/Maven, or network build.

| Sample (`sample:`) | Project | Licence | ~ncloc |
|---|---|---|---|
| `js` | [React](https://github.com/facebook/react) | MIT (`react.LICENSE`) | ~98k |
| `java` | [jackson-databind](https://github.com/FasterXML/jackson-databind) | Apache-2.0 (`jackson.LICENSE`) | ~76k |
| `mixed` **(default)** | jackson-databind: `jackson.zip` (full ~76k) + `jackson-pr.zip` (one package, ~3.5k) | Apache-2.0 (`jackson.LICENSE`) | mix |

`*.zip` hold each project's `scanner-report/` (which embeds the project source), generated
against SonarQube 2026.1. Regenerate by scanning the project with `sonar.scanner.keepReport=true`
(the `analysis-cache2.pb` / `analysis-warnings.pb` entries are excluded — not needed for replay).

`mixed` replays a realistic workload: mostly small PR-sized analyses (`jackson-pr.zip`, the
`node` package scanned on its own) plus the occasional full scan (`jackson.zip`), defaulting
to 80% PRs (`model.pr_fraction`). `jackson-pr.zip` was produced with a CLI scan of just that
package (`sonar.sources=…/databind/node`, `sonar.java.binaries=target/classes`) so the report
genuinely contains only those 27 files.

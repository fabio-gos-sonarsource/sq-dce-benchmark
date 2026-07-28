# Bundled sample seeds (pre-scanned)

These are **pre-generated SonarScanner reports** replayed by the benchmark when
`seed_repo` is empty — so a sample run needs no scanner, JDK/Maven, or network build.

| Sample (`sample:`) | Project | Licence | ~ncloc |
|---|---|---|---|
| `python` | [Rich](https://github.com/Textualize/rich) | MIT (`python.LICENSE`) | ~32k |
| `java` | [Apache Commons Lang](https://github.com/apache/commons-lang) | Apache-2.0 (`java.LICENSE`) | ~34k |

`*.zip` hold each project's `scanner-report/` (which embeds the project source), generated
against SonarQube 2026.1. Regenerate by scanning the project with `sonar.scanner.keepReport=true`.

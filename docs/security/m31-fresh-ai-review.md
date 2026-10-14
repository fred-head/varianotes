# M31 fresh AI security review — open blockers

## Provenance and disposition

- Fresh Codex session: `01a13980-9cee-7aa3-b386-fc165eb942b6`, separate from the implementer conversation.
- Immutable isolated checkout: `dc59d0cdb4f88b158594f02fa007fd097c6a6164`.
- Verified pre-P01 baseline: `060e6367a7905f6306bac7273f19e1681df41f39`.
- Scope: full P01–P17 difference, including all F1–F10 corrections, plus relevant existing boundaries. The P17 changes were assembled locally; PR #24 was not merged.
- Reviewer executed 38 selected existing Go tests serially with persistent disk caches and a bounded memory target; no files or Git state were changed in the review checkout.
- The implementer re-read the cited control flows and confirmed the missing barriers/fences. The negative power-loss and paused-unlock flows were not dynamically reproduced; no real power-cut test was performed.
- R1–R5 remain open. Work stops at these blockers; PRs #30/#31 stay open, PR #24 has no merge approval, productive activation remains disabled, and P18 is unchanged.
- This is an AI source review with the limits stated below, not the required independent reviewer sign-off for P17. No security or merge approval is claimed.

## Reviewer report

**R1 — hoch: Recovery beginnt ohne dauerhaft gesicherte Commit-Entscheidung.**
`internal/encryption/transaction.go`, `CommitContext`:475, `saveRecord`:902, `recoverForVault`:560. Nach sichtbarem Commit-Rename scheitert der Directory-Sync. Recovery verändert trotzdem Live-Dateien. Wird eine Konvertierungsquelle dauerhaft gelöscht und fällt danach das System aus, kann der alte `prepared`-Marker zurückkehren. Prepared-Recovery entfernt dann die verbliebenen Ciphertext-Stages und den neuen Katalog: Datenverlust. **Korrektur:** Commit-Marker vor Forward-Recovery erneut dauerhaft sichern; bei Syncfehler keine Mutation. **Test:** getrennte sichtbare/dauerhafte Zustände, Commit-Syncfehler, Recovery, Quelllöschung, Stromverlust und erneute Recovery.

**R2 — hoch: Protected Trash sichert seine Zielstruktur nicht dauerhaft.**
`internal/files/protected_trash.go`, `MoveToTrashWithCatalog`:100–149/201, `finalizePendingTrash`:1105. Zielverzeichnisse entstehen außerhalb des Journals ohne Parent-Sync; Metadaten erhalten nur einen Datei-Sync. Finalisierung synchronisiert ihre Rename-Eltern ebenfalls nicht. Nach dauerhaftem Quell-Delete und Katalog-Commit kann Stromverlust die ungesicherte Zielstruktur beziehungsweise Restore-Metadaten verlieren lassen. **Korrektur:** Verzeichnisse über `PrepareFilesystem` sichern und Metadaten vor Quelllöschung dauerhaft wiederherstellbar machen. **Test:** Verlust ungesicherter Änderungen nach jedem mkdir, Metadatenpublikation und Finalisierung; Ciphertext und Restore-Zuordnung müssen erhalten bleiben.

**R3 — hoch: Envelope-Replay überspringt eine fehlende Dauerhaftigkeitsbarriere.**
`internal/app/password_change.go`, `vaultPasswordChangeCoordinator.apply`:233; `internal/app/encryption_recovery.go`, `applyRecoveryEnvelope`:361. Beispiel: Neues Envelope dauerhaft gespeichert, Abbruch vor Auth-Commit; Rollback renamt altes Envelope, Prozess endet vor Directory-Sync. Nächster Start sieht die alten Zielbytes, überspringt Sync und löscht das SQLite-Journal. Stromverlust kann danach das neue Envelope zurückbringen, während Auth alt bleibt und der Plan fehlt. Entsperr-/Recovery-Zugang kann auseinanderlaufen. **Korrektur:** Auch Gleichheitszweige vor Journal-Löschung synchronisieren. **Test:** mehrfach unterbrochener Rollback für Passwort- und vorhandene Recovery-Envelopes.

**R4 — mittel: Laufender Unlock kann einen abgeschlossenen Lock/Widerruf überholen.**
`internal/app/handler.go`, Unlock-Handler:889–894; `internal/encryption/key_manager.go`, `Unlock`:110. Gültiger Unlock startet Passwortableitung/Katalogprüfung. Parallel werden Manual Lock, Logout oder Widerruf abgeschlossen. Die alte Anfrage prüft anschließend nur den requestlokalen Principal und installiert erneut Key/Grant ohne ursprüngliches Sperr-/Widerrufstoken. Manual Lock kann aufgehoben werden; nach Logout kann ein Grant für die ungültige Session zurückbleiben. Neue Requests bleiben durch DB-Auth gesperrt. **Korrektur:** Grant-Erteilung atomar gegen den bei Unlock-Beginn erfassten Zustand prüfen. **Test:** Unlock vor Grant pausieren, Lock/Logout/Revoke abschließen, fortsetzen; Key/Grant darf nicht zurückkehren.

**R5 — hoch: Klartext bleibt bei ausgefallenem Lock-Event-Kanal sichtbar.**
`frontend/src/App.tsx`, EventSource-Effekt:282–327, Status-Effekt:407–424, Health-Poll:491–497, Anzeige:1263–1285. Geschützte Notiz öffnen, Netzwerk/SSE verlieren, Tab ohne Aktivität oder Fokuswechsel sichtbar lassen. Server sperrt nach 15 Minuten; Browser erhält kein Event. Ohne Fehlerhandler oder regelmäßige Statusprüfung bleibt `vaultLocked=false`, vorhandener Klartext sichtbar. Editor-Autosperre setzt lediglich Read-only. **Korrektur:** Unsicheren Lock-Zustand durch Ausblenden absichern und verbindungsunabhängige Anzeigegrenze vorsehen; Draft verborgen erhalten. **Test:** SSE-Fehler/Offlinezustand plus 15-Minuten-Timer ohne Fokuswechsel für Editor, Konflikt und Suche.

**1. Freigabeblocker**

R1–R3 gefährden Datenerhalt beziehungsweise Schlüsselwiederherstellung; R4–R5 verletzen die geforderte Sperrgrenze. Alle fünf sind vor Produktionsaktivierung zu beheben und gezielt abzusichern. Aktivierung blieb deaktiviert; P18 unberührt.

**2. Sonstige Befunde**

Keine zusätzlichen ausreichend abgesicherten Befunde aufgenommen.

**3. Geprüfte Bereiche ohne zusätzlichen Befund**

Prüfstand `dc59d0cdb4f88b158594f02fa007fd097c6a6164`; aus Git verifizierte Basis vor P01: `060e6367a7905f6306bac7273f19e1681df41f39`. Vergleich: 93 geänderte Dateien plus relevante bestehende Grenzen.

Geprüft: Crypto/age, Katalog und registrierte Vault-Identität, Pfad-/Symlink-/Hardlink-Grenzen, Versionskonflikte, Schutzregeln/gemeinsame Attachments, Move/Trash/Restore, Search, Exportgrants/Throttling, Rewrap/Recovery sowie Auth/Session/CSRF. Kein zusätzlicher Aktivierungspfad gefunden.

Eigene **38 bestehende Go-Tests**, seriell mit `-p 1 -count=1`: 10 Encryption, 18 Files, 10 App; alle bestanden, einschließlich offizieller age-Wiederherstellung. HEAD und sauberer Arbeitsbaum abschließend bestätigt; keine Änderungen.

**4. Nicht prüfbare Bereiche und Grenzen**

Alle fünf Negativabläufe sind **statisch hergeleitet, nicht dynamisch reproduziert**. Bestehende erfolgreiche Tests bilden diese konkreten Fehlerfolgen nicht ab. Keine realen Stromverlustversuche, vollständige Race-/Lastsuite oder Browser-/Proxy-Ausfalltests; Frontendquellen und Tests nur gelesen. Frühere Reports, Eltern-Tests und CI zählen nicht als eigene Belege. Konsistenter vollständiger Rollback älterer Vault-Daten, kompromittierter entsperrter Server und P18-Betreiberworkflow bleiben Prüfgrenzen. Keine allgemeine Sicherheits- oder Mergefreigabe.

# M31 fresh AI review after R1–R5 corrections

**Drei hohe Freigabeblocker und ein weiterer Dauerhaftigkeitsbefund wurden gefunden.** Die folgenden Abläufe sind aus dem Quelltext hergeleitet; ich habe keine Tests ausgeführt und keine Laufzeitreproduktion vorgenommen.

**1. Hoch — Export-Recovery kann den Schlüssel eines laufenden Saves löschen**

Betroffen sind `handleNotebookExport` in [export_handler.go:29](../../internal/app/export_handler.go#L29), `catalogForKey` in [protected_notes.go:84](../../internal/app/protected_notes.go#L84) sowie `recoverForVault` und `cleanupRecord` in [transaction.go:549](../../internal/encryption/transaction.go#L549).

Jeder Export erzeugt eigene Repository-, Access- und Journalinstanzen mit eigenen Mutexen. Deren Recovery kann deshalb gleichzeitig mit einem Save des aktiven Repositorys laufen.

Nachvollziehbarer Ablauf:

1. Save A stellt Journal und Stages im Zustand `prepared` bereit.
2. Export E liest diesen Zustand und wird vor der Bereinigung unterbrochen.
3. A schreibt `committed`, validiert den neuen Katalog und installiert den neuen Ciphertext.
4. E bereinigt anhand seines alten Records. Dabei entfernt es den noch benötigten Katalog-Stage und anschließend das Journal.
5. A scheitert beim Katalog-Rename.

Ein Save erzeugt einen neuen Dateischlüssel. Dessen Envelope liegt im neuen Katalog. Nach diesem Ablauf passt der alte Live-Katalog nicht zum neuen Ciphertext; das alte Dateiobjekt wurde überschrieben, und der neue Envelope fehlt. Auch die Neustart-Recovery hat kein Journal mehr.

**Voraussetzungen und Auswirkungen:** Ein entsperrter geschützter Vault und konkurrierende Requests innerhalb eines einzigen unterstützten Serverprozesses genügen. Bereits der Export-GET vor der Passwortaufforderung führt Recovery aus. Ein Stromausfall ist nicht erforderlich. Der Ablauf kann unwiederherstellbaren Inhaltsverlust verursachen.

**Korrektur:** Alle Zugriffe auf denselben Vault müssen eine gemeinsame, langlebige Synchronisierung für Repository, Journal, Prepare, Commit und Recovery verwenden. Recovery muss laufende Transaktionen von verwaisten Transaktionen unterscheiden. Nur denselben Journalmutex zu teilen reicht nicht aus, solange eine aktive `prepared`-Transaktion zwischen Prepare und Commit bereinigt werden kann.

**Nötiger Regressionstest:** HTTP-Export und Export-Reauth parallel zu einem geschützten Save, mit deterministischen Pausen nach dem Lesen von `prepared` und nach der Ciphertext-Installation. Anschließend müssen Inhalt, Envelope, Katalog und Neustart-Recovery vollständig erhalten sein. Vergleichbare Move-/Restore-Abläufe sind ebenfalls abzudecken.

**2. Hoch — Trash-Reconciliation kann mit einem alten Katalog die Wiederherstellungszuordnung löschen**

Der Handler lädt den Katalog vor dem Repository-Lock in [handler.go:1711](../../internal/app/handler.go#L1711). `TrashItemsWithCatalog` übernimmt diesen Snapshot und ruft unter seinen Locks die Reconciliation auf. Diese entscheidet in [protected_trash.go:999](../../internal/files/protected_trash.go#L999) ausschließlich anhand des übergebenen Katalogs, ob eine pending Trashoperation committed wurde.

Nachvollziehbarer Ablauf:

1. Trash-Abfrage B lädt den alten Katalog und wartet vor dem Trash-Lock.
2. Operation A installiert den Zielciphertext und entfernt die Quelle.
3. Ein Fehler vor `transaction.catalog.rename` lässt das committed Journal und die pending `metadata.json` bestehen.
4. B läuft mit seinem alten Snapshot weiter. Darin fehlen die neuen Trashobjekte, weshalb B das pending Verzeichnis einschließlich der Wiederherstellungszuordnung entfernt.
5. Eine spätere Journal-Recovery kann Ciphertext und Katalog vervollständigen, aber `OriginalPath` nicht wiederherstellen.

Die spätere Prüfung erhält verbleibende Dateien konservativ, blockiert jedoch Trashobjekte ohne Metadaten. Das verhindert weitere Löschung, repariert aber die bereits verlorene Zuordnung nicht. Eine ähnliche Lücke besteht bei einem Fehler vor der abschließenden Metadatenverschiebung.

**Voraussetzungen und Auswirkungen:** Konkurrierende Trashzugriffe plus ein unterbrochener Commit beziehungsweise Finalisierungsschritt. Die verschlüsselten Bytes können erhalten bleiben; die Anwendung kann den Eintrag jedoch nicht mehr regulär an seinen ursprünglichen Ort zurückstellen. Manuelle Datenrettung wird erforderlich.

**Korrektur:** Recovery, aktuellen Katalog und Reconciliation unter einer gemeinsamen Vault-Synchronisierung feststellen. Pending Metadaten bei committed Journal oder unklarem Zustand konservativ erhalten. Eine Aktualitätsprüfung des Live-Katalogs allein genügt nicht, wenn dieser nach einem Teilcommit noch alt ist.

**Nötiger Regressionstest:** B nimmt seinen Snapshot vor A auf; A scheitert vor dem Katalog-Rename nach Quellenlöschung; B reconciliert anschließend. Neustart, Recovery und Restore müssen danach exakte Bytes und ursprüngliche Pfade erhalten.

**3. Hoch — Dokumentierter CLI-Sessionwiderruf erreicht den laufenden KeyManager nicht; R4 bleibt offen**

`auth reset-mfa` ruft in [main.go:122](../../cmd/varianotes/main.go#L122) `ResetMFA` auf. Dieses widerruft die Sessions dauerhaft in [mfa.go:427](../../internal/auth/mfa.go#L427), benachrichtigt aber den KeyManager des laufenden Servers nicht. Die dokumentierte Ausführung erfolgt über `docker compose exec`.

Der HTTP-Unlock prüft die Session in [handler.go:906](../../internal/app/handler.go#L906) und installiert danach die Berechtigung über `CompleteUnlock`. Dessen Zulassungstoken prüft nur den Zustand des jeweiligen KeyManagers.

Nachvollziehbarer Ablauf: Ein Unlock erhält bei `Sessions.Active` noch `true` und wird unmittelbar danach unterbrochen. Der CLI-Befehl widerruft die Session erfolgreich. Anschließend akzeptiert `CompleteUnlock` weiterhin den unveränderten Token und installiert eine Schlüsselberechtigung für die widerrufene Session.

Auch bereits zugelassene Antworten und Exportstreams bleiben betroffen: `guardLease` und `CheckCurrent` prüfen die Berechtigung im KeyManager, ohne den CLI-Widerruf zu sehen.

**Voraussetzungen und Auswirkungen:** Der dokumentierte Operatorbefehl läuft bei aktivem Server und trifft einen bereits zugelassenen geschützten Request. Neue Requests werden durch die dauerhafte Sessionprüfung abgewiesen; laufende geschützte Arbeit kann dennoch nach abgeschlossenem Widerruf veröffentlichen, und Schlüsselberechtigungen bleiben im Speicher erhalten.

**Korrektur:** CLI-Widerrufe mit dem laufenden Server und dessen Grant-/Publikationsbarriere koordinieren oder solche CLI-Änderungen verbindlich nur bei gestopptem Server zulassen. Eine zusätzliche unkoordinierte Sessionabfrage würde das Zeitfenster lediglich verschieben.

**Nötiger Regressionstest:** Ein zweiter Auth-Service auf derselben Datenbank führt `ResetMFA` aus, während Unlock nach der letzten Sessionprüfung sowie eine Antwort beziehungsweise ein Stream vor der nächsten Veröffentlichung pausiert sind. Danach dürfen weder Grantinstallation noch geschützte Veröffentlichung gelingen.

**4. Mittel — Beim ersten geschützten Upload fehlt die Parent-Fsync-Barriere**

`PrepareAssetUploadWithCatalog` erzeugt das neue `Note.assets`-Verzeichnis in [protected.go:492](../../internal/files/protected.go#L492). `contentStore.mkdir` synchronisiert dessen Elternverzeichnis nicht. Der Upload verwendet anschließend `journal.Prepare`, wodurch die Verzeichnisanlage nicht im Dateisystemjournal enthalten ist.

Stage und Veröffentlichung synchronisieren `Note.assets`, aber nicht dessen Parent. Der sichtbare Parent-Sync in diesem Uploadpfad gehört ausschließlich zum Rollback.

**Ablauf und Auswirkungen:** Ein erster geschützter Upload kann erfolgreich bestätigt, der Katalog dauerhaft installiert und das Journal entfernt werden, obwohl die Dauerhaftigkeit des neuen Verzeichniseintrags im Parent nicht zugesichert ist. Bei einem entsprechenden Speicher-/Stromausfall kann das Assetverzeichnis fehlen, während der Katalog noch auf dessen Dateien verweist. Ob ein konkretes Dateisystem diesen Zustand tatsächlich verliert, wurde nicht geprüft.

**Korrektur:** Die Verzeichnisanlage journalen oder vor Commit eine Parent-Barriere mit Fehlerweitergabe herstellen. Wiederholungen müssen diese Barriere auch bei bereits sichtbarem Verzeichnis erneut ausführen.

**Nötiger Regressionstest:** Erster geschützter Upload mit Prüfung der Parent-Sync-Reihenfolge, injiziertem Syncfehler und anschließendem Retry/Neustart.

**Freigabeblocker**

Befunde 1–3 blockieren aus dieser Prüfung die Sicherheitsabnahme beziehungsweise produktive Aktivierung von M31. Die vorhandene Aktivierungssperre muss bestehen bleiben. Dies ist keine Merge- oder Expertenfreigabe.

**Sonstige Befunde**

Befund 4 ist eine zusätzliche offene Dauerhaftigkeitslücke. In den gelesenen Tests fand ich keine gezielte Absicherung der vier beschriebenen Abläufe. Die vorhandenen Restart-, Syncfehler-, Snapshot- und Widerrufstests ersetzen diese Interleavings nicht.

**Bewertung R1–R5**

| Punkt | Quellenbewertung |
|---|---|
| R1 | Die erneute dauerhafte Commitentscheidung und Publikationsbarrieren vor Quellenlöschung sind im isolierten Recoverypfad vorhanden. Befund 1 verhindert eine insgesamt belastbare Recoverygarantie. |
| R2 | Serielle Trash-Barrieren und Finalisierungswiederholungen sind verbessert. Wegen Befund 2 weiterhin offen. |
| R3 | Gleiche Zielbytes werden erneut dauerhaft geschrieben; bereits fehlende Envelopes lösen erneut Directory-Sync aus. Im geprüften Replaypfad kein weiterer Befund. |
| R4 | Token, Generation, globaler Epoch und Commitgate schließen die geprüften HTTP-Widerrufspfade. Der CLI-Pfad aus Befund 3 bleibt offen. |
| R5 | Lokale Anzeige-Frist, Offline-/SSE-Fehlerbehandlung, verspätete Antworten und bestätigte Aktivitätsverlängerung sind im Quelltext abgesichert. Echte Browserabläufe bleiben unbestätigt. |

Die zugehörigen Regressionstests wurden **als Quelltext gelesen**, darunter Recovery-Durability, Envelope-Replay, Unlock-Fences und `App.lock-security.test.tsx`. Ich habe keinen Testlauf als eigenen Nachweis.

**Weitere geprüfte Bereiche ohne zusätzlichen Befund**

- Kryptografische Formate, authentifizierte Entschlüsselung, Objektbindung, Katalogintegrität, registrierte Vaultidentität und Ablehnung unbekannter Formate.
- Pfadbegrenzung, Symlinkprüfungen, Hardlinkablehnung bei Konvertierung und fehlender Klartextfallback bei beschädigtem oder unbekanntem Ciphertext.
- Gemeinsame Assets, Referenzprüfungen, geschützte Move-/Restorepfade und verschlüsselte Stages.
- Authentifizierungs-/CSRF-Grenzen, Exportpasswortprüfung, einmalige gebundene Exportgrants und Credential-Throttling.
- Suche ohne persistenten Klartextindex, API-No-Store, NetworkOnly-Service-Worker-Konfiguration und versteckte RAM-Drafts.
- Geschlossene produktive Aktivierung über UI/API/CLI; dokumentierte Grenzen für frühere Klartextkopien und kompromittierte entsperrte Server.

**Prüfumfang und Grenzen**

Prüfrahmen war der vollständige P01–P17-Diff einschließlich F1–F10/R1–R5 und relevanter bestehender Grenzen. Die Baseline `060e6367a7905f6306bac7273f19e1681df41f39` wurde über die Git-Historie verifiziert. Der Zusatzdiff zu `7d0e68f…` fügt nur zwei Kommentarzeilen und einen Warteschritt in `App.test.tsx` hinzu.

Nicht durchgeführt wurden Laufzeitreproduktionen, Test- oder Race-Suiten, echte Browserprüfungen mit Milkdown/Bildansicht, BFCache-/Suspend-Prüfungen und physische Stromausfalltests. Die beschriebenen Interleavings benötigen gezielte Regressionstests. Diese KI-Quellenprüfung bestätigt keine allgemeine Sicherheit.

Zum Abschluss verifiziert: HEAD weiterhin **`f29ecf3d4d5a3788565a5216aa8ff8e2eda44f3b`**, Worktree sauber, keine Änderungen.

## Provenance and disposition (implementer record)

- Fresh source-review session: `01a13a59-9c7c-7073-9792-1f97f8818894`.
- Immutable isolated candidate: `f29ecf3d4d5a3788565a5216aa8ff8e2eda44f3b`;
  independently verified baseline: `060e6367a7905f6306bac7273f19e1681df41f39`.
- Complete P01–P17 plus F1–F10/R1–R5; PR #24 was assembled locally and not merged.
  The three-line test-only addition at `7d0e68f7a8caed682ee2dd8861a0050a9798d5d7`
  was separately read by the reviewer. Production sources are identical.
- The report is reproduced above with portable source links. The reviewer
  corrected the direction of the supplementary test diff in a read-only
  follow-up; the findings were unchanged. The implementation conversation and
  its passing tests were not treated as reviewer evidence.
- Review findings 1–4 are referenced as B1–B4 in the remediation record. B1/B2
  share missing coordination across active transaction/recovery and catalog
  snapshot boundaries. B3 leaves R4's online CLI boundary open; B4 concerns a
  directory-publication barrier outside the patched Trash paths.
- The implementer re-read the cited Export/Journal/Trash/CLI control flows.
  These additional negative interleavings and the upload power-loss state had
  not been dynamically reproduced or fixed on the reviewed candidate. The
  report is not a general safety certification or merge approval.
- Work stopped at the reported blockers on that candidate. At the time, the
  correction PRs were open; PR #24 remains open, productive activation is
  disabled and P18 is unchanged. Corrections for B1/B2, B3 and B4 were later
  merged into `main` in PRs #38, #39 and #40. A fresh full-scope review of the
  updated candidate remains necessary under the Alpha 3 P17 rule.

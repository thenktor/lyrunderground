// Package ui contains the Fyne user interface of the application.
package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"lyrunderground/internal/library"
	"lyrunderground/internal/lrclib"
)

type trackState struct {
	info           library.Track
	lyrics         string
	originalLyrics string
	serverLyrics   string

	status     string
	importance widget.Importance

	busy               bool
	checked            bool
	exists             bool
	instrument         bool
	instrumentalUpload bool
	forceUpload        bool
	recordID           int64
}

type uploadOutcome int

const (
	outcomeFailed uploadOutcome = iota
	outcomeUploaded
	outcomeAlreadyPresent
)

// LRCLIB does not document a concrete propagation delay for newly published
// lyrics, so give users a conservative upper bound.
const lyricsAvailabilityNote = "Es kann bis zu 24 Stunden dauern, bis die Lyrics auf LRCLIB verfügbar sind."

const maxLyricsBytes = 256 * 1024

const readOnlyLyricsForegroundMix = 0.65

var changedLyricsColor = color.NRGBA{R: 0xF2, G: 0xC9, B: 0x4C, A: 0xFF}

type readOnlyLyricsTheme struct {
	fyne.Theme
	dimmed bool
}

func (t readOnlyLyricsTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if t.dimmed || name != theme.ColorNameDisabled {
		return t.Theme.Color(name, variant)
	}
	disabled := t.Theme.Color(theme.ColorNameDisabled, variant)
	foreground := t.Theme.Color(theme.ColorNameForeground, variant)
	return blendColor(disabled, foreground, readOnlyLyricsForegroundMix)
}

func blendColor(base, overlay color.Color, overlayWeight float64) color.Color {
	b := color.NRGBAModel.Convert(base).(color.NRGBA)
	o := color.NRGBAModel.Convert(overlay).(color.NRGBA)
	mix := func(a, c uint8) uint8 {
		return uint8(float64(a)*(1-overlayWeight) + float64(c)*overlayWeight)
	}
	return color.NRGBA{
		R: mix(b.R, o.R),
		G: mix(b.G, o.G),
		B: mix(b.B, o.B),
		A: b.A,
	}
}

type bulkJob struct {
	index        int
	info         library.Track
	lyrics       string
	required     bool
	instrumental bool
	forceUpload  bool
}

// App stores the state of the running application. Tracks and their UI state
// are only modified from the Fyne main thread; background work reports back
// through fyne.Do / fyne.DoAndWait.
type App struct {
	win       fyne.Window
	client    *lrclib.Client
	autoCheck bool

	folderEntry  *widget.Entry
	chooseBtn    *widget.Button
	fileBtn      *widget.Button
	scanBtn      *widget.Button
	uploadAllBtn *widget.Button
	clearBtn     *widget.Button
	cancelBtn    *widget.Button

	status   *widget.Label
	progress *widget.ProgressBar

	list       *widget.List
	listHeader *widget.Label
	tracks     []*trackState
	selected   int

	detailTags          *widget.Label
	detailStatus        *widget.Label
	detailLyrics        *widget.Entry
	detailServerLyrics  *widget.Entry
	detailServerTab     *container.ThemeOverride
	detailTabs          *container.AppTabs
	copyServerLyricsBtn *widget.Button
	lyricsChangedLabel  *canvas.Text
	serverLyricsDimmed  bool
	detailInstrumental  *widget.Check
	detailForceUpload   *widget.Check
	updatingLyrics      bool
	detailCheck         *widget.Button
	detailUpload        *widget.Button

	scanRunning bool
	scanCancel  context.CancelFunc

	bulkRunning bool
	bulkCancel  context.CancelFunc
}

// New builds the user interface for the given window.
func New(win fyne.Window) *App {
	a := &App{
		win:       win,
		client:    lrclib.NewClient(),
		autoCheck: true,
		selected:  -1,
	}

	a.folderEntry = widget.NewEntry()
	a.folderEntry.SetPlaceHolder("/Pfad/zum/Albumordner")
	a.folderEntry.OnSubmitted = func(string) { a.scanFolder() }

	a.chooseBtn = widget.NewButton("Albumordner öffnen", a.chooseFolder)
	a.fileBtn = widget.NewButton("Datei öffnen", a.chooseFile)
	a.scanBtn = widget.NewButton("Erneut scannen", a.scanFolder)
	a.scanBtn.Importance = widget.HighImportance

	a.uploadAllBtn = widget.NewButton("Alle hochladen", func() { a.startBulk(true) })
	a.clearBtn = widget.NewButton("Liste leeren", a.clearList)
	a.cancelBtn = widget.NewButton("Abbrechen", a.cancelCurrent)
	a.cancelBtn.Importance = widget.WarningImportance

	a.status = widget.NewLabel("Bereit. Öffne einen Albumordner.")
	a.status.Wrapping = fyne.TextWrapWord
	a.status.Importance = widget.MediumImportance
	a.progress = widget.NewProgressBar()
	a.progress.Min = 0
	a.progress.Max = 1
	a.progress.SetValue(0)

	a.listHeader = widget.NewLabelWithStyle("Noch keine Dateien geladen", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	a.list = widget.NewList(
		func() int { return len(a.tracks) },
		func() fyne.CanvasObject { return newTrackListItem() },
		func(id widget.ListItemID, item fyne.CanvasObject) {
			updateTrackListItem(a, item, int(id))
		},
	)
	a.list.HideSeparators = false
	a.list.OnSelected = func(id widget.ListItemID) {
		a.selectTrack(int(id))
	}

	a.detailTags = widget.NewLabel("")
	a.detailTags.TextStyle = fyne.TextStyle{Monospace: true}
	a.detailTags.Wrapping = fyne.TextWrapWord
	a.detailTags.Selectable = true
	a.detailStatus = widget.NewLabel("")
	a.detailStatus.Wrapping = fyne.TextWrapWord

	a.detailLyrics = widget.NewMultiLineEntry()
	a.detailLyrics.SetPlaceHolder("Lyrics einfügen – entweder Klartext oder LRC mit Zeitstempeln wie [00:12.34]…")
	a.detailLyrics.Wrapping = fyne.TextWrapWord
	a.detailLyrics.OnChanged = func(text string) {
		if a.updatingLyrics || a.selected < 0 || a.selected >= len(a.tracks) {
			return
		}
		limited, truncated := limitLyricsText(text)
		if truncated {
			a.updatingLyrics = true
			a.detailLyrics.SetText(limited)
			a.detailLyrics.Text = limited
			a.detailLyrics.Refresh()
			a.updatingLyrics = false
			a.setGlobalStatus("Lyrics auf 256 KiB begrenzt.", widget.WarningImportance)
			text = limited
		}
		a.tracks[a.selected].lyrics = text
		a.updateLyricsChangedLabel(a.tracks[a.selected])
		a.refreshDetailControls()
	}

	a.detailServerLyrics = widget.NewMultiLineEntry()
	a.detailServerLyrics.SetPlaceHolder("Noch keine LRCLIB-Lyrics geladen.")
	a.detailServerLyrics.Wrapping = fyne.TextWrapWord

	a.copyServerLyricsBtn = widget.NewButtonWithIcon("Kopieren", theme.ContentCopyIcon(), a.copyServerLyrics)
	a.copyServerLyricsBtn.Disable()
	a.lyricsChangedLabel = canvas.NewText("Lyrics geändert", changedLyricsColor)
	a.lyricsChangedLabel.TextSize = theme.TextSize()
	a.lyricsChangedLabel.Hide()
	a.detailServerLyrics.Disable()

	a.detailServerTab = container.NewThemeOverride(
		a.detailServerLyrics,
		readOnlyLyricsTheme{Theme: fyne.CurrentApp().Settings().Theme()},
	)
	a.detailTabs = container.NewAppTabs(
		container.NewTabItem("Tag", a.detailLyrics),
		container.NewTabItem("LRCLIB", a.detailServerTab),
	)

	a.detailInstrumental = widget.NewCheck("Instrumental (keine Lyrics)", func(checked bool) {
		if a.selected < 0 || a.selected >= len(a.tracks) {
			return
		}
		a.tracks[a.selected].instrumentalUpload = checked
		a.refreshDetailControls()
	})

	a.detailForceUpload = widget.NewCheck("Hochladen erzwingen", func(checked bool) {
		if a.selected < 0 || a.selected >= len(a.tracks) {
			return
		}
		a.tracks[a.selected].forceUpload = checked
		a.refreshDetailControls()
	})

	a.detailCheck = widget.NewButton("Prüfen", func() {
		if a.selected >= 0 {
			a.checkOne(a.selected)
		}
	})
	a.detailUpload = widget.NewButton("Hochladen", func() {
		if a.selected >= 0 {
			a.uploadOne(a.selected)
		}
	})
	a.detailUpload.Importance = widget.HighImportance

	heading := widget.NewLabelWithStyle("LRCLIB Lyrics Uploader", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	hint := widget.NewLabel("Öffne einen Albumordner oder einzelne Audiodateien (auch per Drag & Drop), wähle einen Track aus und lade die Lyrics zu lrclib.net hoch.")
	hint.Wrapping = fyne.TextWrapWord

	folderRow := container.NewBorder(
		nil, nil, nil,
		container.NewHBox(a.chooseBtn, a.fileBtn, a.scanBtn),
		a.folderEntry,
	)
	top := container.NewVBox(heading, hint, folderRow)

	listFooter := container.NewVBox(
		widget.NewSeparator(),
		container.NewGridWithColumns(2, a.uploadAllBtn, a.clearBtn),
	)
	left := container.NewBorder(a.listHeader, listFooter, nil, nil, a.list)

	lyricsRow := container.NewHBox(
		widget.NewLabel("Lyrics:"),
		a.detailInstrumental,
		layout.NewSpacer(),
		a.lyricsChangedLabel,
		a.copyServerLyricsBtn,
	)
	detailTop := container.NewVBox(
		a.detailTags,
		a.detailStatus,
		widget.NewSeparator(),
		lyricsRow,
	)
	detailButtons := container.NewHBox(a.detailCheck, a.detailUpload, a.detailForceUpload)
	detail := container.NewBorder(detailTop, detailButtons, nil, nil, a.detailTabs)

	split := container.NewHSplit(left, detail)
	split.SetOffset(0.36)

	progressRow := container.NewBorder(nil, nil, nil, a.cancelBtn, a.progress)
	bottom := container.NewVBox(widget.NewSeparator(), a.status, progressRow)
	content := container.NewBorder(top, bottom, nil, nil, split)
	win.SetContent(content)

	win.SetOnDropped(a.handleDrop)
	win.SetCloseIntercept(func() {
		a.cancelCurrent()
		win.Close()
	})

	a.clearDetail()
	a.updateControls()
	return a
}

func (a *App) chooseFolder() {
	folderDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		if uri == nil {
			return
		}
		a.folderEntry.SetText(uri.Path())
		// The folder is an album, so immediately show its files.
		a.scanFolder()
	}, a.win)
	folderDialog.SetTitleText("Albumordner öffnen")
	folderDialog.Resize(a.dialogSize())
	folderDialog.Show()
}

func (a *App) chooseFile() {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() {
		return
	}
	fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		a.addFile(path)
	}, a.win)
	fileDialog.SetTitleText("Audiodatei öffnen")
	fileDialog.SetFilter(storage.NewExtensionFileFilter([]string{".mp3", ".ogg", ".opus", ".flac", ".m4a"}))
	fileDialog.Resize(a.dialogSize())
	fileDialog.Show()
}

func (a *App) dialogSize() fyne.Size {
	windowSize := a.win.Canvas().Size()
	width := windowSize.Width * 0.9
	if width < 900 {
		width = 900
	}
	height := windowSize.Height * 0.9
	if height < 600 {
		height = 600
	}
	return fyne.NewSize(width, height)
}

func (a *App) addFile(path string) {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() {
		return
	}
	track, err := library.ReadTrack(path)
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}

	if index := a.trackIndex(track.Path); index >= 0 {
		a.selected = index
		a.list.Select(index)
		a.list.ScrollTo(index)
		a.updateDetail()
		a.setGlobalStatus("Datei ist bereits in der Liste.", widget.MediumImportance)
		return
	}

	added, firstIndex := a.appendTracks([]library.Track{track})
	if added == 0 {
		return
	}
	a.setGlobalStatus(fmt.Sprintf("%s hinzugefügt. Prüfe LRCLIB…", track.DisplayName()), widget.MediumImportance)
	a.startAutoCheckRange(firstIndex, len(a.tracks))
}

func (a *App) trackIndex(path string) int {
	for index, track := range a.tracks {
		if sameFile(track.info.Path, path) {
			return index
		}
	}
	return -1
}

func (a *App) updateListHeader() {
	if len(a.tracks) == 0 {
		a.listHeader.SetText("Noch keine Dateien geladen")
		return
	}
	a.listHeader.SetText(fmt.Sprintf("Dateien (%d)", len(a.tracks)))
}

func (a *App) appendTracks(tracks []library.Track) (added, firstIndex int) {
	firstIndex = -1
	for _, track := range tracks {
		if a.trackIndex(track.Path) >= 0 {
			continue
		}
		a.tracks = append(a.tracks, newTrackState(track))
		if firstIndex < 0 {
			firstIndex = len(a.tracks) - 1
		}
		added++
	}
	if added == 0 {
		return added, firstIndex
	}
	a.updateListHeader()
	a.list.Refresh()
	a.selected = firstIndex
	a.list.Select(firstIndex)
	a.list.ScrollTo(firstIndex)
	a.updateDetail()
	a.updateControls()
	return added, firstIndex
}

func sameFile(a, b string) bool {
	aAbs, aErr := filepath.Abs(a)
	bAbs, bErr := filepath.Abs(b)
	if aErr == nil && bErr == nil {
		return aAbs == bAbs
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (a *App) handleDrop(_ fyne.Position, uris []fyne.URI) {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() {
		a.setGlobalStatus("Bitte warten – es läuft bereits eine Aktion.", widget.WarningImportance)
		return
	}

	files := make([]string, 0, len(uris))
	dirs := make([]string, 0, len(uris))
	ignored := 0
	for _, uri := range uris {
		if uri.Scheme() != "file" {
			ignored++
			continue
		}
		path := uri.Path()
		info, err := os.Stat(path)
		if err != nil {
			ignored++
			continue
		}
		if info.IsDir() {
			dirs = append(dirs, path)
			continue
		}
		if library.IsSupportedFile(path) {
			files = append(files, path)
		} else {
			ignored++
		}
	}

	if len(files) == 0 && len(dirs) == 0 {
		a.setGlobalStatus("Keine unterstützten Dateien oder Ordner abgelegt.", widget.WarningImportance)
		return
	}

	// A single dropped folder behaves like "Albumordner öffnen" and
	// replaces the current list. Everything else is appended.
	if len(dirs) == 1 && len(files) == 0 {
		a.folderEntry.SetText(dirs[0])
		a.scanFolder()
		return
	}
	if len(dirs) == 0 {
		a.dropFiles(files, ignored)
		return
	}
	a.addDroppedItems(files, dirs, ignored)
}

func (a *App) dropFiles(files []string, ignored int) {
	tracks := make([]library.Track, 0, len(files))
	failed := 0
	for _, file := range files {
		track, err := library.ReadTrack(file)
		if err != nil {
			failed++
			continue
		}
		tracks = append(tracks, track)
	}
	added, firstIndex := a.appendTracks(tracks)

	parts := make([]string, 0, 3)
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d Datei(en) hinzugefügt", added))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d Fehler", failed))
	}
	if ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignoriert", ignored))
	}
	if len(parts) == 0 {
		parts = append(parts, "Keine neuen Dateien hinzugefügt")
	}
	importance := widget.MediumImportance
	if failed > 0 || ignored > 0 {
		importance = widget.WarningImportance
	}
	a.setGlobalStatus(strings.Join(parts, ", ")+".", importance)
	if added > 0 {
		a.startAutoCheckRange(firstIndex, len(a.tracks))
	}
}

func (a *App) addDroppedItems(files, dirs []string, ignored int) {
	ctx, cancel := context.WithCancel(context.Background())
	a.scanRunning = true
	a.scanCancel = cancel
	a.progress.Min = 0
	a.progress.Max = float64(len(files) + len(dirs))
	a.progress.SetValue(0)
	a.setGlobalStatus("Verarbeite abgelegte Ordner und Dateien…", widget.MediumImportance)
	a.updateControls()

	go func() {
		tracks := make([]library.Track, 0)
		failed := 0
		done := 0
		for _, dir := range dirs {
			if ctx.Err() != nil {
				break
			}
			found, fileErrors, err := library.Scan(ctx, dir, nil)
			if err != nil {
				failed++
			} else {
				tracks = append(tracks, found...)
			}
			failed += len(fileErrors)
			done++
			current := done
			fyne.Do(func() {
				a.progress.SetValue(float64(current))
				a.setGlobalStatus(fmt.Sprintf("Durchsuche Ordner %d/%d…", current, len(dirs)), widget.MediumImportance)
			})
		}
		for _, file := range files {
			if ctx.Err() != nil {
				break
			}
			track, err := library.ReadTrack(file)
			if err != nil {
				failed++
			} else {
				tracks = append(tracks, track)
			}
			done++
		}
		cancelled := ctx.Err() != nil

		fyne.Do(func() {
			a.scanRunning = false
			a.scanCancel = nil
			a.progress.SetValue(float64(len(files) + len(dirs)))
			if cancelled {
				a.setGlobalStatus("Drag & Drop abgebrochen.", widget.WarningImportance)
				a.updateControls()
				return
			}
			added, firstIndex := a.appendTracks(tracks)
			parts := make([]string, 0, 3)
			if added > 0 {
				parts = append(parts, fmt.Sprintf("%d Datei(en) hinzugefügt", added))
			}
			if failed > 0 {
				parts = append(parts, fmt.Sprintf("%d Fehler", failed))
			}
			if ignored > 0 {
				parts = append(parts, fmt.Sprintf("%d ignoriert", ignored))
			}
			if len(parts) == 0 {
				parts = append(parts, "Keine neuen Dateien hinzugefügt")
			}
			importance := widget.MediumImportance
			if failed > 0 || ignored > 0 {
				importance = widget.WarningImportance
			}
			a.updateControls()
			a.setGlobalStatus(strings.Join(parts, ", ")+".", importance)
			if added > 0 {
				a.startAutoCheckRange(firstIndex, len(a.tracks))
			}
		})
	}()
}

func (a *App) scanFolder() {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() {
		return
	}
	folder := strings.TrimSpace(a.folderEntry.Text)
	if folder == "" {
		dialog.ShowError(errors.New("Bitte zuerst einen Albumordner auswählen."), a.win)
		return
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		dialog.ShowError(fmt.Errorf("Der Ordner %q kann nicht gelesen werden.", folder), a.win)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.scanRunning = true
	a.scanCancel = cancel
	a.tracks = nil
	a.selected = -1
	a.list.UnselectAll()
	a.list.Refresh()
	a.updateListHeader()
	a.clearDetail()
	a.setGlobalStatus("Durchsuche Albumordner…", widget.MediumImportance)
	a.progress.Max = 1
	a.progress.SetValue(0)
	a.updateControls()

	go func() {
		var lastUpdate time.Time
		tracks, fileErrors, scanErr := library.Scan(ctx, folder, func(done, total int, path string) {
			if path != "" && done != total && time.Since(lastUpdate) < 75*time.Millisecond {
				return
			}
			lastUpdate = time.Now()
			fyne.Do(func() {
				if total > 0 {
					a.progress.Max = float64(total)
					a.progress.SetValue(float64(done))
				} else {
					a.progress.Max = 1
					a.progress.SetValue(0)
				}
				if path != "" {
					a.setGlobalStatus(fmt.Sprintf("Lese Dateien: %d/%d – %s", done+1, total, filepath.Base(path)), widget.MediumImportance)
				}
			})
		})

		fyne.Do(func() {
			a.scanRunning = false
			a.scanCancel = nil

			if scanErr != nil {
				a.tracks = nil
				a.selected = -1
				a.list.Refresh()
				a.updateListHeader()
				a.clearDetail()
				if errors.Is(scanErr, context.Canceled) {
					a.setGlobalStatus("Scan abgebrochen.", widget.WarningImportance)
				} else {
					a.setGlobalStatus("Scan fehlgeschlagen: "+scanErr.Error(), widget.DangerImportance)
					dialog.ShowError(scanErr, a.win)
				}
				a.updateControls()
				return
			}

			a.tracks = make([]*trackState, 0, len(tracks))
			for _, track := range tracks {
				a.tracks = append(a.tracks, newTrackState(track))
			}
			a.selected = -1
			a.list.Refresh()
			a.updateListHeader()
			a.progress.Max = 1
			a.progress.SetValue(1)
			switch {
			case len(tracks) == 0:
				a.setGlobalStatus("Keine unterstützten Musikdateien gefunden (mp3, ogg, opus, flac, m4a).", widget.WarningImportance)
			case len(fileErrors) > 0:
				a.setGlobalStatus(fmt.Sprintf("%d Titel gefunden. %d Datei(en) konnten nicht gelesen werden.", len(tracks), len(fileErrors)), widget.WarningImportance)
			default:
				a.setGlobalStatus(fmt.Sprintf("%d Titel gefunden. Wähle eine Datei aus der Liste.", len(tracks)), widget.MediumImportance)
			}

			if len(a.tracks) > 0 {
				a.selected = 0
				a.list.Select(0)
				a.list.ScrollToTop()
				a.updateDetail()
			} else {
				a.clearDetail()
			}
			a.updateControls()
			a.startAutoCheckRange(0, len(a.tracks))
		})
	}()
}

func (a *App) clearList() {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() {
		return
	}
	a.tracks = nil
	a.selected = -1
	a.list.UnselectAll()
	a.list.Refresh()
	a.updateListHeader()
	a.clearDetail()
	a.progress.Max = 1
	a.progress.SetValue(0)
	a.setGlobalStatus("Liste geleert.", widget.MediumImportance)
	a.updateControls()
}

func (a *App) cancelCurrent() {
	if a.scanCancel != nil {
		a.scanCancel()
	}
	if a.bulkCancel != nil {
		a.bulkCancel()
	}
	if a.scanRunning || a.bulkRunning {
		a.setGlobalStatus("Abbruch angefordert…", widget.WarningImportance)
	}
}

func (a *App) setGlobalStatus(text string, importance widget.Importance) {
	a.status.Importance = importance
	a.status.SetText(text)
	a.status.Refresh()
}

func (a *App) updateControls() {
	globalBusy := a.scanRunning || a.bulkRunning
	busy := globalBusy || a.anyRowBusy()
	hasTracks := len(a.tracks) > 0

	if busy {
		a.folderEntry.Disable()
		a.chooseBtn.Disable()
		a.fileBtn.Disable()
		a.scanBtn.Disable()
	} else {
		a.folderEntry.Enable()
		a.chooseBtn.Enable()
		a.fileBtn.Enable()
		a.scanBtn.Enable()
	}

	if busy || !hasTracks {
		a.uploadAllBtn.Disable()
		a.clearBtn.Disable()
	} else {
		a.uploadAllBtn.Enable()
		a.clearBtn.Enable()
	}

	if globalBusy {
		a.cancelBtn.Enable()
	} else {
		a.cancelBtn.Disable()
	}

	a.refreshDetailControls()
}

func (a *App) anyRowBusy() bool {
	for _, track := range a.tracks {
		if track.busy {
			return true
		}
	}
	return false
}

func (a *App) currentTrack(index int, st *trackState) bool {
	return index >= 0 && index < len(a.tracks) && a.tracks[index] == st
}

func (a *App) refreshTrack(index int) {
	if index < 0 || index >= len(a.tracks) {
		return
	}
	a.list.RefreshItem(index)
	if index == a.selected {
		a.updateDetail()
	}
}

func (a *App) checkOne(index int) {
	if !a.canOperateOn(index) {
		return
	}
	st := a.tracks[index]
	if !st.info.HasRequiredMetadata() {
		a.setMissingMetadataError(index)
		return
	}
	info := st.info
	st.busy = true
	st.checked = false
	st.exists = false
	st.status = "Prüfe LRCLIB…"
	st.importance = widget.MediumImportance
	a.refreshTrack(index)
	a.updateControls()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		record, err := a.client.Check(ctx, infoOf(info))
		fyne.Do(func() {
			if !a.currentTrack(index, st) {
				return
			}
			st.busy = false
			a.applyCheckOutcome(st, record, err)
			a.refreshTrack(index)
			a.updateControls()
		})
	}()
}

func (a *App) uploadOne(index int) {
	if !a.canOperateOn(index) {
		return
	}
	st := a.tracks[index]
	if !st.info.HasRequiredMetadata() {
		a.setMissingMetadataError(index)
		return
	}
	if !st.forceUpload && (!st.checked || st.exists) {
		st.status = "Bitte zuerst prüfen – Upload erst nach bestätigtem Fehlen der Lyrics (oder „Hochladen erzwingen“ aktivieren)."
		st.importance = widget.WarningImportance
		a.refreshTrack(index)
		return
	}
	lyrics := lyricsForUpload(st)
	if lyrics == "" && !st.instrumentalUpload {
		st.status = "Keine Lyrics eingegeben – Upload nicht möglich (oder „Instrumental“ aktivieren)."
		st.importance = widget.DangerImportance
		a.refreshTrack(index)
		return
	}

	info := st.info
	synced := library.IsSyncedLyrics(lyrics)
	instrumental := st.instrumentalUpload
	force := st.forceUpload
	st.busy = true
	st.status = "Prüfe, ob Lyrics bereits vorhanden sind…"
	st.importance = widget.MediumImportance
	a.refreshTrack(index)
	a.updateControls()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()

		outcome, record, err := a.uploadCore(ctx, info, lyrics, synced, instrumental, force, func(message string) {
			fyne.DoAndWait(func() {
				if !a.currentTrack(index, st) {
					return
				}
				st.status = message
				st.importance = widget.MediumImportance
				a.refreshTrack(index)
			})
		})

		fyne.Do(func() {
			if !a.currentTrack(index, st) {
				return
			}
			st.busy = false
			a.applyUploadOutcome(st, outcome, record, err)
			a.refreshTrack(index)
			a.updateControls()
		})
	}()
}

func (a *App) canOperateOn(index int) bool {
	if index < 0 || index >= len(a.tracks) {
		return false
	}
	if a.scanRunning || a.bulkRunning {
		return false
	}
	return !a.tracks[index].busy
}

func (a *App) setMissingMetadataError(index int) {
	st := a.tracks[index]
	missing := st.info.MissingFields()
	st.status = "Upload nicht möglich – fehlende Angaben: " + strings.Join(missing, ", ")
	st.importance = widget.DangerImportance
	a.refreshTrack(index)
}

// lyricsForUpload always uses the editable tag field. The LRCLIB tab is a
// read-only reference and must never become the upload source implicitly.
func lyricsForUpload(st *trackState) string {
	return strings.TrimSpace(st.lyrics)
}

func sameLyricsText(a, b string) bool {
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		return strings.TrimSpace(s)
	}
	return normalize(a) == normalize(b)
}

func (a *App) updateLyricsChangedLabel(st *trackState) {
	if sameLyricsText(st.lyrics, st.originalLyrics) {
		a.lyricsChangedLabel.Hide()
	} else {
		a.lyricsChangedLabel.Show()
	}
	a.lyricsChangedLabel.Refresh()
}

func lyricsFromRecord(record *lrclib.Record) string {
	if record == nil {
		return ""
	}
	lyrics := ""
	if record.SyncedLyrics != nil && strings.TrimSpace(*record.SyncedLyrics) != "" {
		lyrics = *record.SyncedLyrics
	} else if record.PlainLyrics != nil && strings.TrimSpace(*record.PlainLyrics) != "" {
		lyrics = *record.PlainLyrics
	}
	lyrics = strings.ReplaceAll(lyrics, "\r\n", "\n")
	lyrics = strings.ReplaceAll(lyrics, "\r", "\n")
	return strings.TrimSpace(lyrics)
}

func limitLyricsText(text string) (string, bool) {
	if len(text) <= maxLyricsBytes {
		return text, false
	}
	text = text[:maxLyricsBytes]
	for len(text) > 0 && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

func (a *App) applyCheckOutcome(st *trackState, record *lrclib.Record, err error) {
	// Defensive guard: a successful lookup without a record must never reach
	// the record-based branches below, which would dereference nil.
	if err == nil && record == nil {
		err = errors.New("LRCLIB hat keine gültige Antwort geliefert")
	}
	switch {
	case errors.Is(err, lrclib.ErrNotFound):
		st.checked = true
		st.exists = false
		st.instrument = false
		st.recordID = 0
		st.serverLyrics = ""
		st.status = "Noch nicht auf LRCLIB vorhanden – Upload möglich."
		st.importance = widget.MediumImportance
	case err != nil:
		st.checked = false
		switch {
		case errors.Is(err, context.Canceled):
			st.status = "Prüfung abgebrochen."
			st.importance = widget.WarningImportance
		case errors.Is(err, context.DeadlineExceeded):
			st.status = "Prüfung fehlgeschlagen: Zeitüberschreitung."
			st.importance = widget.DangerImportance
		default:
			st.status = "Prüfung fehlgeschlagen: " + err.Error()
			st.importance = widget.DangerImportance
		}
	default:
		st.checked = true
		st.recordID = record.ID
		st.instrument = record.Instrumental
		if record.HasLyrics() {
			st.exists = true
			serverLyrics, truncated := limitLyricsText(lyricsFromRecord(record))
			st.serverLyrics = serverLyrics
			if serverLyrics != "" {
				st.status = fmt.Sprintf("Bereits auf LRCLIB vorhanden (ID %d) – LRCLIB-Lyrics im Tab verfügbar.", record.ID)
			} else {
				st.status = fmt.Sprintf("Bereits auf LRCLIB vorhanden (ID %d).", record.ID)
			}
			if truncated {
				st.status += " Lyrics wurden auf 256 KiB begrenzt."
			}
			st.importance = widget.SuccessImportance
		} else {
			st.exists = false
			st.serverLyrics = ""
			if record.Instrumental {
				st.status = fmt.Sprintf("Auf LRCLIB als instrumental markiert (ID %d) – Upload möglich.", record.ID)
			} else {
				st.status = fmt.Sprintf("Track auf LRCLIB vorhanden, aber ohne Lyrics (ID %d) – Upload möglich.", record.ID)
			}
			st.importance = widget.MediumImportance
		}
	}
}

func (a *App) uploadCore(ctx context.Context, info library.Track, lyrics string, synced, instrumental, force bool, onStatus func(string)) (uploadOutcome, *lrclib.Record, error) {
	if onStatus != nil {
		onStatus("Prüfe, ob Lyrics bereits vorhanden sind…")
	}
	record, err := a.client.Check(ctx, infoOf(info))
	if err == nil {
		if record.HasLyrics() && !force {
			return outcomeAlreadyPresent, record, nil
		}
	} else if !errors.Is(err, lrclib.ErrNotFound) {
		return outcomeFailed, nil, err
	}

	if onStatus != nil {
		onStatus("Erzeuge Publish-Token (Proof-of-Work, kann einige Sekunden dauern)…")
	}
	err = a.client.Publish(ctx, lrclib.PublishRequest{
		TrackInfo:    publishTrackInfo(info, record),
		Lyrics:       lyrics,
		Synced:       synced,
		Instrumental: instrumental,
	})
	if err != nil {
		return outcomeFailed, record, err
	}
	return outcomeUploaded, record, nil
}

// publishTrackInfo prefers the metadata returned by LRCLIB when a track was
// found. This keeps a new revision attached to the existing track even if the
// local album tag differs from LRCLIB's stored value.
func publishTrackInfo(info library.Track, record *lrclib.Record) lrclib.TrackInfo {
	result := infoOf(info)
	if record == nil {
		return result
	}
	if strings.TrimSpace(record.ArtistName) != "" {
		result.ArtistName = record.ArtistName
	}
	if strings.TrimSpace(record.TrackName) != "" {
		result.TrackName = record.TrackName
	}
	if strings.TrimSpace(record.AlbumName) != "" {
		result.AlbumName = record.AlbumName
	}
	return result
}

func (a *App) applyUploadOutcome(st *trackState, outcome uploadOutcome, record *lrclib.Record, err error) {
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			st.status = "Upload abgebrochen."
			st.importance = widget.WarningImportance
		case errors.Is(err, context.DeadlineExceeded):
			st.status = "Upload fehlgeschlagen: Zeitüberschreitung."
			st.importance = widget.DangerImportance
		default:
			st.status = "Upload fehlgeschlagen: " + err.Error()
			st.importance = widget.DangerImportance
		}
		return
	}

	switch outcome {
	case outcomeUploaded:
		forced := st.forceUpload
		st.checked = true
		st.exists = true
		st.forceUpload = false
		if st.instrumentalUpload {
			// No lyrics were uploaded, so the edit state of the tag field is
			// unrelated and keeps its "Lyrics geändert" marker.
			st.serverLyrics = ""
		} else {
			st.serverLyrics = st.lyrics
			// The uploaded text is now the stored state of this track, so the
			// "Lyrics geändert" marker must not stay visible.
			st.originalLyrics = st.lyrics
		}
		if record != nil {
			st.recordID = record.ID
		}
		switch {
		case st.instrumentalUpload && forced:
			st.status = "Erfolgreich als instrumental (erzwungen) auf LRCLIB hochgeladen. " + lyricsAvailabilityNote
		case forced:
			st.status = "Erfolgreich (erzwungen) auf LRCLIB hochgeladen. " + lyricsAvailabilityNote
		case st.instrumentalUpload:
			st.status = "Erfolgreich als instrumental auf LRCLIB hochgeladen. " + lyricsAvailabilityNote
		default:
			st.status = "Erfolgreich auf LRCLIB hochgeladen. " + lyricsAvailabilityNote
		}
		st.importance = widget.SuccessImportance
	case outcomeAlreadyPresent:
		st.checked = true
		st.exists = true
		if record != nil {
			st.serverLyrics, _ = limitLyricsText(lyricsFromRecord(record))
			st.recordID = record.ID
			st.status = fmt.Sprintf("Bereits auf LRCLIB vorhanden (ID %d) – nichts hochgeladen.", record.ID)
		} else {
			st.status = "Bereits auf LRCLIB vorhanden – nichts hochgeladen."
		}
		st.importance = widget.SuccessImportance
	default:
		st.status = "Upload ohne Ergebnis beendet."
		st.importance = widget.WarningImportance
	}
}

func (a *App) startBulk(upload bool) {
	indices := make([]int, len(a.tracks))
	for index := range a.tracks {
		indices[index] = index
	}
	a.startBulkFor(indices, upload)
}

func (a *App) startBulkFor(indices []int, upload bool) {
	if a.scanRunning || a.bulkRunning || a.anyRowBusy() || len(indices) == 0 {
		return
	}

	jobs := make([]bulkJob, 0, len(indices))
	for _, index := range indices {
		if index < 0 || index >= len(a.tracks) {
			continue
		}
		st := a.tracks[index]
		jobs = append(jobs, bulkJob{
			index:        index,
			info:         st.info,
			lyrics:       lyricsForUpload(st),
			required:     st.info.HasRequiredMetadata(),
			instrumental: st.instrumentalUpload,
			forceUpload:  st.forceUpload,
		})
	}
	if len(jobs) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.bulkRunning = true
	a.bulkCancel = cancel
	a.progress.Min = 0
	a.progress.Max = float64(len(jobs))
	a.progress.SetValue(0)
	a.setGlobalStatus(fmt.Sprintf("Starte %s von %d Titeln…", bulkVerb(upload), len(jobs)), widget.MediumImportance)
	a.updateControls()
	a.list.Refresh()

	go a.runBulk(ctx, jobs, upload)
}

// startAutoCheckRange checks newly opened tracks immediately, without the user
// having to press "Prüfen" first.
func (a *App) startAutoCheckRange(first, end int) {
	if !a.autoCheck {
		return
	}
	if first < 0 {
		first = 0
	}
	if end > len(a.tracks) {
		end = len(a.tracks)
	}
	if first >= end {
		return
	}
	indices := make([]int, 0, end-first)
	for index := first; index < end; index++ {
		indices = append(indices, index)
	}
	a.startBulkFor(indices, false)
}

func bulkVerb(upload bool) string {
	if upload {
		return "Upload"
	}
	return "Prüfung"
}

func (a *App) runBulk(ctx context.Context, jobs []bulkJob, upload bool) {
	var checked, uploaded, present, skipped, failed int
	cancelled := false

	for i, job := range jobs {
		if ctx.Err() != nil {
			cancelled = true
			break
		}

		// Mark the row as busy and update the global progress text. All access
		// to the track state happens on the Fyne main thread.
		fyne.DoAndWait(func() {
			if job.index >= 0 && job.index < len(a.tracks) {
				st := a.tracks[job.index]
				st.busy = true
				st.status = "Wird verarbeitet…"
				st.importance = widget.MediumImportance
				a.refreshTrack(job.index)
			}
			a.setGlobalStatus(fmt.Sprintf("%s %d/%d: %s", bulkVerb(upload), i+1, len(jobs), job.info.DisplayName()), widget.MediumImportance)
		})

		if !job.required {
			fyne.DoAndWait(func() {
				st := a.tracks[job.index]
				st.busy = false
				missing := st.info.MissingFields()
				st.status = "Übersprungen – fehlende Angaben: " + strings.Join(missing, ", ")
				st.importance = widget.DangerImportance
				a.refreshTrack(job.index)
			})
			skipped++
			fyne.Do(func() { a.progress.SetValue(float64(i + 1)) })
			continue
		}

		if upload && job.lyrics == "" && !job.instrumental {
			fyne.DoAndWait(func() {
				st := a.tracks[job.index]
				st.busy = false
				st.status = "Übersprungen – keine Lyrics eingegeben."
				st.importance = widget.WarningImportance
				a.refreshTrack(job.index)
			})
			skipped++
			fyne.Do(func() { a.progress.SetValue(float64(i + 1)) })
			continue
		}

		if upload {
			outcome, record, err := a.uploadCore(ctx, job.info, job.lyrics, library.IsSyncedLyrics(job.lyrics), job.instrumental, job.forceUpload, func(message string) {
				fyne.DoAndWait(func() {
					st := a.tracks[job.index]
					st.status = message
					st.importance = widget.MediumImportance
					a.refreshTrack(job.index)
				})
			})

			if err != nil && ctx.Err() != nil {
				fyne.DoAndWait(func() {
					st := a.tracks[job.index]
					st.busy = false
					st.status = "Abgebrochen."
					st.importance = widget.WarningImportance
					a.refreshTrack(job.index)
				})
				cancelled = true
				break
			}

			fyne.DoAndWait(func() {
				st := a.tracks[job.index]
				st.busy = false
				a.applyUploadOutcome(st, outcome, record, err)
				a.refreshTrack(job.index)
			})
			switch {
			case err != nil:
				failed++
			case outcome == outcomeUploaded:
				uploaded++
			case outcome == outcomeAlreadyPresent:
				present++
			}
		} else {
			record, err := a.client.Check(ctx, infoOf(job.info))
			if err != nil && ctx.Err() != nil {
				fyne.DoAndWait(func() {
					st := a.tracks[job.index]
					st.busy = false
					st.status = "Abgebrochen."
					st.importance = widget.WarningImportance
					a.refreshTrack(job.index)
				})
				cancelled = true
				break
			}

			fyne.DoAndWait(func() {
				st := a.tracks[job.index]
				st.busy = false
				a.applyCheckOutcome(st, record, err)
				a.refreshTrack(job.index)
			})
			if err != nil && !errors.Is(err, lrclib.ErrNotFound) {
				failed++
			} else {
				checked++
				if err == nil && record.HasLyrics() {
					present++
				}
			}
		}

		fyne.Do(func() { a.progress.SetValue(float64(i + 1)) })
	}

	fyne.Do(func() {
		a.bulkRunning = false
		a.bulkCancel = nil
		a.updateControls()
		a.list.Refresh()

		var summary string
		switch {
		case upload && cancelled:
			summary = fmt.Sprintf("Upload abgebrochen: %d hochgeladen, %d bereits vorhanden, %d übersprungen, %d Fehler.", uploaded, present, skipped, failed)
		case upload:
			summary = fmt.Sprintf("Upload abgeschlossen: %d hochgeladen, %d bereits vorhanden, %d übersprungen, %d Fehler.", uploaded, present, skipped, failed)
		case cancelled:
			summary = fmt.Sprintf("Prüfung abgebrochen: %d geprüft, %d mit vorhandenen Lyrics, %d übersprungen, %d Fehler.", checked, present, skipped, failed)
		default:
			summary = fmt.Sprintf("Prüfung abgeschlossen: %d geprüft, %d mit vorhandenen Lyrics, %d übersprungen, %d Fehler.", checked, present, skipped, failed)
		}
		importance := widget.MediumImportance
		switch {
		case cancelled || failed > 0:
			importance = widget.WarningImportance
		case upload && (uploaded > 0 || present > 0):
			importance = widget.SuccessImportance
		}
		a.setGlobalStatus(summary, importance)
		if a.selected >= 0 && a.selected < len(a.tracks) {
			a.updateDetail()
		}
	})
}

func newTrackState(track library.Track) *trackState {
	lyrics, truncated := limitLyricsText(track.Lyrics)
	st := &trackState{
		info:           track,
		lyrics:         lyrics,
		originalLyrics: lyrics,
	}
	switch {
	case !track.HasRequiredMetadata():
		st.status = "Nicht hochladbar – fehlende Angaben: " + strings.Join(track.MissingFields(), ", ")
		st.importance = widget.DangerImportance
	case truncated:
		st.status = "Lyrics-Tag auf 256 KiB begrenzt."
		st.importance = widget.WarningImportance
	case len(track.Warnings) > 0:
		st.status = "Bereit. Hinweis: " + track.Warnings[0]
		st.importance = widget.WarningImportance
	case strings.TrimSpace(track.Lyrics) != "":
		st.status = "Lyrics aus Datei-Tag geladen. Noch nicht geprüft."
		st.importance = widget.MediumImportance
	default:
		st.status = "Bereit. Noch nicht geprüft."
		st.importance = widget.MediumImportance
	}
	return st
}

func infoOf(track library.Track) lrclib.TrackInfo {
	return lrclib.TrackInfo{
		ArtistName: track.Artist,
		TrackName:  track.Title,
		AlbumName:  track.Album,
		Duration:   track.Duration,
	}
}

func (a *App) selectTrack(index int) {
	if index < 0 || index >= len(a.tracks) {
		return
	}
	a.selected = index
	a.updateDetail()
}

func (a *App) updateDetail() {
	if a.selected < 0 || a.selected >= len(a.tracks) {
		a.clearDetail()
		return
	}
	st := a.tracks[a.selected]
	a.detailTags.SetText(detailTagText(st.info))
	a.detailStatus.Importance = st.importance
	a.detailStatus.SetText(st.status)
	a.detailStatus.Refresh()
	if a.detailLyrics.Text != st.lyrics {
		a.updatingLyrics = true
		a.detailLyrics.SetText(st.lyrics)
		a.updatingLyrics = false
	}
	if a.detailServerLyrics.Text != st.serverLyrics {
		a.detailServerLyrics.SetText(st.serverLyrics)
	}
	if strings.TrimSpace(st.serverLyrics) == "" {
		a.copyServerLyricsBtn.Disable()
	} else {
		a.copyServerLyricsBtn.Enable()
	}
	a.updateLyricsChangedLabel(st)
	if a.detailInstrumental.Checked != st.instrumentalUpload {
		a.detailInstrumental.SetChecked(st.instrumentalUpload)
	}
	if a.detailForceUpload.Checked != st.forceUpload {
		a.detailForceUpload.SetChecked(st.forceUpload)
	}
	a.refreshDetailControls()
}

func (a *App) copyServerLyrics() {
	text := a.detailServerLyrics.Text
	if strings.TrimSpace(text) == "" {
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(text)
	a.setGlobalStatus("LRCLIB-Lyrics in die Zwischenablage kopiert.", widget.SuccessImportance)
}

func (a *App) clearDetail() {
	a.detailTags.SetText("")
	a.detailStatus.Importance = widget.MediumImportance
	a.detailStatus.SetText("Öffne einen Albumordner und wähle eine Datei aus der Liste.")
	a.detailStatus.Refresh()
	if a.detailLyrics.Text != "" {
		a.updatingLyrics = true
		a.detailLyrics.SetText("")
		a.updatingLyrics = false
	}
	if a.detailServerLyrics.Text != "" {
		a.detailServerLyrics.SetText("")
	}
	a.copyServerLyricsBtn.Disable()
	a.lyricsChangedLabel.Hide()
	if a.detailInstrumental.Checked {
		a.detailInstrumental.SetChecked(false)
	}
	if a.detailForceUpload.Checked {
		a.detailForceUpload.SetChecked(false)
	}
	a.detailLyrics.Disable()
	a.detailInstrumental.Disable()
	a.detailForceUpload.Disable()
	a.detailCheck.Disable()
	a.detailUpload.Disable()
}

func (a *App) refreshDetailControls() {
	if a.selected < 0 || a.selected >= len(a.tracks) {
		a.detailLyrics.Disable()
		a.detailInstrumental.Disable()
		a.detailForceUpload.Disable()
		a.detailCheck.Disable()
		a.detailUpload.Disable()
		return
	}
	st := a.tracks[a.selected]
	busy := a.scanRunning || a.bulkRunning || st.busy
	canOperate := !busy && st.info.HasRequiredMetadata()

	if canOperate {
		a.detailCheck.Enable()
	} else {
		a.detailCheck.Disable()
	}
	if busy {
		a.detailInstrumental.Disable()
	} else {
		a.detailInstrumental.Enable()
	}
	if busy {
		a.detailForceUpload.Disable()
	} else {
		a.detailForceUpload.Enable()
	}

	canUpload := canOperate && (st.forceUpload || (st.checked && !st.exists)) && (st.instrumentalUpload || strings.TrimSpace(st.lyrics) != "")
	if canUpload {
		a.detailUpload.Enable()
	} else {
		a.detailUpload.Disable()
	}

	if busy || st.instrumentalUpload {
		a.detailLyrics.Disable()
	} else {
		a.detailLyrics.Enable()
	}
	a.updateServerLyricsTheme(st.instrumentalUpload)
}

func (a *App) updateServerLyricsTheme(dimmed bool) {
	if a.detailServerTab == nil || a.serverLyricsDimmed == dimmed {
		return
	}
	a.serverLyricsDimmed = dimmed
	a.detailServerTab.Theme = readOnlyLyricsTheme{
		Theme:  fyne.CurrentApp().Settings().Theme(),
		dimmed: dimmed,
	}
	a.detailServerTab.Refresh()
}

func newTrackListItem() fyne.CanvasObject {
	text := widget.NewRichText(
		&widget.TextSegment{Text: ""},
		&widget.TextSegment{Text: ""},
	)
	text.Truncation = fyne.TextTruncateEllipsis
	return text
}

func updateTrackListItem(app *App, item fyne.CanvasObject, index int) {
	text, ok := item.(*widget.RichText)
	if !ok {
		return
	}
	if index < 0 || index >= len(app.tracks) {
		text.Segments = nil
		text.Refresh()
		return
	}
	track := app.tracks[index].info
	// Two paragraphs render with a much smaller gap than two separate labels
	// and keep the list compact. Duration and format intentionally remain in
	// the detail pane only.
	text.Segments = []widget.RichTextSegment{
		&widget.TextSegment{
			Text:  track.DisplayName(),
			Style: widget.RichTextStyle{TextStyle: fyne.TextStyle{Bold: true}},
		},
		&widget.TextSegment{
			Text:  track.FileName,
			Style: widget.RichTextStyle{ColorName: theme.ColorNameDisabled, SizeName: theme.SizeNameCaptionText},
		},
	}
	text.Refresh()
}

func detailTagText(track library.Track) string {
	return fmt.Sprintf(
		"Datei:       %s\nArtist:      %s\nTitel:       %s\nAlbum:       %s\nDauer:       %s\nFormat:      %s\nLyrics-Tag:  %s",
		valueOrDash(track.FileName),
		valueOrDash(track.Artist),
		valueOrDash(track.Title),
		valueOrDash(track.Album),
		formatDuration(track.Duration),
		track.Format,
		lyricsTagInfo(track.Lyrics),
	)
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "–"
	}
	return value
}

func lyricsTagInfo(lyrics string) string {
	lyrics = strings.TrimSpace(lyrics)
	if lyrics == "" {
		return "nicht vorhanden"
	}
	kind := "Text"
	if library.IsSyncedLyrics(lyrics) {
		kind = "LRC"
	}
	lines := strings.Count(lyrics, "\n") + 1
	if lines == 1 {
		return fmt.Sprintf("vorhanden (%s, 1 Zeile)", kind)
	}
	return fmt.Sprintf("vorhanden (%s, %d Zeilen)", kind, lines)
}

func formatDuration(duration time.Duration) string {
	seconds := int(duration.Round(time.Second).Seconds())
	if seconds <= 0 {
		return "Dauer unbekannt"
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

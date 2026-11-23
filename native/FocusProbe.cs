using System;
using System.Windows.Automation;
using System.Windows.Automation.Text;
using System.Threading;
using System.Web.Script.Serialization;
using System.Runtime.InteropServices;
using System.Text;

internal static class FocusProbe
{
    [DllImport("user32.dll")] private static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr window, out uint process);
    [DllImport("user32.dll")] private static extern bool IsWindow(IntPtr window);
    [StructLayout(LayoutKind.Sequential)] private struct GUIInfo {
        public uint size, flags;
        public IntPtr active, focus, capture, menu, move, caret;
        public int left, top, right, bottom;
    }
    [DllImport("user32.dll")] private static extern bool GetGUIThreadInfo(uint thread, ref GUIInfo info);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr window, StringBuilder name, int size);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern IntPtr FindWindowEx(IntPtr parent, IntPtr after, string className, string title);
    [DllImport("user32.dll", EntryPoint="SendMessageW", CharSet=CharSet.Unicode)] private static extern IntPtr ReadText(IntPtr window, uint message, IntPtr size, StringBuilder text);
    [DllImport("user32.dll", EntryPoint="SendMessageW")] private static extern IntPtr GetSelection(IntPtr window, uint message, out int start, out int end);
    [DllImport("user32.dll", EntryPoint="SendMessageW")] private static extern IntPtr SendMessage(IntPtr window, uint message, IntPtr w, IntPtr l);
    [DllImport("user32.dll", EntryPoint="SendMessageW", CharSet=CharSet.Unicode)] private static extern IntPtr ReplaceText(IntPtr window, uint message, IntPtr undo, string text);
    [StructLayout(LayoutKind.Sequential)] private struct KEYBDINPUT { public ushort key; public ushort scan; public uint flags; public uint time; public IntPtr extra; }
    [StructLayout(LayoutKind.Explicit, Size=32)] private struct INPUTUNION { [FieldOffset(0)] public KEYBDINPUT keyboard; }
    [StructLayout(LayoutKind.Sequential)] private struct INPUT { public uint type; public INPUTUNION data; }
    [DllImport("user32.dll", SetLastError=true)] private static extern uint SendInput(uint count, INPUT[] inputs, int size);
    [DllImport("user32.dll", SetLastError=true)] private static extern bool OpenClipboard(IntPtr owner);
    [DllImport("user32.dll", SetLastError=true)] private static extern bool CloseClipboard();
    [DllImport("user32.dll", SetLastError=true)] private static extern IntPtr GetClipboardData(uint format);
    [DllImport("user32.dll", SetLastError=true)] private static extern bool IsClipboardFormatAvailable(uint format);
    [DllImport("kernel32.dll", SetLastError=true)] private static extern IntPtr GlobalLock(IntPtr handle);
    [DllImport("kernel32.dll", SetLastError=true)] private static extern bool GlobalUnlock(IntPtr handle);
    private static AutomationElement tracked;
    private static ValuePattern valuePattern;
    private static TextPattern textPattern;
    private static string prefix, suffix;
    private static IntPtr window, native, inputWindow;
    private static uint ownerProcess;
    private static int insertionStart;
    private static string diagnostic;
    private static string pendingExpected, pendingText;
    // pastedNow — текст положен вставкой из буфера, а не набором. Вставка
    // атомарна: она либо прошла, либо нет, и перечитывать поле ради неё незачем.
    private static bool pastedNow;
    private static string seen;
    private static int seenAt;
    public sealed class Request {
        public string operation { get; set; }
        public string expected { get; set; }
        public string text { get; set; }
        public ulong window { get; set; }
    }
    private static string Canonical(string text) { return (text ?? "").Replace("\u00a0", " ").Replace("\r\n", "\n"); }
    // ClassNameOf — имя класса окна: по нему видно, то ли это окно, что было.
    private static string ClassNameOf(IntPtr handle) {
        if (handle == IntPtr.Zero || !IsWindow(handle)) return "none";
        StringBuilder name = new StringBuilder(128);
        GetClassName(handle, name, name.Capacity);
        return name.ToString();
    }
    private static IntPtr FocusWindow() {
        uint process;
        GUIInfo info = new GUIInfo();
        info.size = (uint)Marshal.SizeOf(info);
        GetGUIThreadInfo(GetWindowThreadProcessId(GetForegroundWindow(), out process), ref info);
        return info.focus;
    }
    private static string Document() {
        if (!IsWindow(window)) throw new ElementNotAvailableException();
        uint process;
        GetWindowThreadProcessId(window, out process);
        if (process != ownerProcess) throw new ElementNotAvailableException();
        if (native != IntPtr.Zero) {
            if (!IsWindow(native)) throw new ElementNotAvailableException();
            int size = (int)SendMessage(native, 0xe, IntPtr.Zero, IntPtr.Zero);
            if (size < 0 || size > 262144) throw new InvalidOperationException();
            StringBuilder buffer = new StringBuilder(size + 1);
            ReadText(native, 0xd, new IntPtr(buffer.Capacity), buffer);
            return buffer.ToString();
        }
        if (!IsEditable(tracked)) throw new ElementNotAvailableException();
        object pattern;
        if (!tracked.TryGetCurrentPattern(ValuePattern.Pattern, out pattern)) throw new ElementNotAvailableException();
        valuePattern = (ValuePattern)pattern;
        return Canonical(valuePattern.Current.Value);
    }
    // След вставки: каждое решение и каждая попытка набора пишутся в файл с
    // временем. Нужен, чтобы видеть, кто и сколько раз набирает текст, когда в
    // поле появляются дубли.
    private static void Trace(string message) {
        try {
            string dir = System.IO.Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Dictation");
            System.IO.Directory.CreateDirectory(dir);
            System.IO.File.AppendAllText(System.IO.Path.Combine(dir, "insertion-trace.log"),
                DateTime.Now.ToString("HH:mm:ss.fff") + " pid=" + System.Diagnostics.Process.GetCurrentProcess().Id + " " + message + Environment.NewLine);
        } catch {}
    }
    private static bool Check(string expected) {
        return Same(Document(), expected);
    }
    private static bool Same(string actual, string expected) {
        string wanted = prefix + expected + suffix;
        bool matches = native != IntPtr.Zero ? actual == wanted : actual.TrimEnd('\r','\n') == wanted.TrimEnd('\r','\n');
        diagnostic = "mode=" + (native != IntPtr.Zero ? "native" : "value") + ";document=" + matches
            + ";actualLength=" + actual.Length + ";expectedLength=" + wanted.Length + Where(actual, wanted);
        return matches;
    }
    // Где именно поле разошлось с ожидаемым текстом. По длинам видно только «на
    // сколько», а причина лечится по тому, «что»: потерянный знак, чужая буква
    // или хвост, которого поле ещё не показало.
    private static string Where(string actual, string wanted) {
        int limit = Math.Min(actual.Length, wanted.Length);
        for (int i = 0; i < limit; i++) {
            if (actual[i] == wanted[i]) continue;
            return ";at=" + i + ";field=" + ((int)actual[i]) + ";wanted=" + ((int)wanted[i]);
        }
        if (actual.Length == wanted.Length) return "";
        string tail = actual.Length > wanted.Length ? actual.Substring(limit) : wanted.Substring(limit);
        if (tail.Length > 24) tail = tail.Substring(0, 24);
        return ";at=" + limit + ";tail=" + Convert.ToBase64String(Encoding.UTF8.GetBytes(tail));
    }
    // Common — сколько знаков две строки совпадают от начала. По одному этому
    // числу видно, разошлось ли поле в самом начале (чужая правка) или ближе к
    // концу (наша речь, дописанная после того, как приложение её запомнило).
    private static int Common(string one, string other) {
        int limit = Math.Min(one.Length, other.Length);
        for (int i = 0; i < limit; i++) {
            if (one[i] != other[i]) return i;
        }
        return limit;
    }
    // Head — первые знаки строки в base64: в журнал русский текст иначе не
    // доходит, а по нему видно, что именно в поле лежит.
    private static string Head(string text) {
        string head = text.Length > 32 ? text.Substring(0, 32) : text;
        return Convert.ToBase64String(Encoding.UTF8.GetBytes(head));
    }
    // Owned — то, что лежит в поле между краями. Пусто, если края не целы: тогда
    // это чужое поле, и трогать его нельзя.
    private static string Owned(string current) {
        if (current.Length < prefix.Length + suffix.Length) return "";
        if (!current.StartsWith(prefix, StringComparison.Ordinal)) return "";
        if (!current.EndsWith(suffix, StringComparison.Ordinal)) return "";
        return current.Substring(prefix.Length, current.Length - prefix.Length - suffix.Length);
    }
    private static string Initialize(Request request) {
        pendingExpected=pendingText=null;
        window = new IntPtr((long)request.window);
        if (GetForegroundWindow() != window) return "unsupported-capture-focus";
        GetWindowThreadProcessId(window, out ownerProcess);
        for (int attempt = 0; attempt < 6; attempt++) {
            tracked = AutomationElement.FocusedElement;
            if (IsEditable(tracked)) break;
            Thread.Sleep(40);
        }
        if (!IsEditable(tracked)) return "unsupported-editability";
        native = FocusWindow();
        inputWindow = native;
        StringBuilder name = new StringBuilder(128);
        GetClassName(native, name, name.Capacity);
        string className = name.ToString().ToLowerInvariant();
        string document;
        if (className == "edit" || className.StartsWith("richedit") || className.Contains(".edit.")) {
            int end;
            GetSelection(native, 0xb0, out insertionStart, out end);
            if (end != insertionStart) return "unsupported-selection";
            document = Document();
            prefix = document.Substring(0, insertionStart);
            suffix = document.Substring(insertionStart);
        } else {
            native = IntPtr.Zero;
            object pattern;
            if (!tracked.TryGetCurrentPattern(ValuePattern.Pattern, out pattern)) return "unsupported-addressed-write";
            valuePattern = (ValuePattern)pattern;
            document = Document();
            if (document.Trim('\r','\n').Length == 0) document = "";
            insertionStart = 0;
            if (document.Length != 0) {
                if (!tracked.TryGetCurrentPattern(TextPattern.Pattern, out pattern)) return "unsupported-selection";
                TextPattern text = (TextPattern)pattern;
                TextPatternRange[] selections = text.GetSelection();
                if (selections.Length != 1 || selections[0].CompareEndpoints(TextPatternRangeEndpoint.Start, selections[0], TextPatternRangeEndpoint.End) != 0)
                    return "unsupported-selection";
                TextPatternRange before = text.DocumentRange.Clone();
                before.MoveEndpointByRange(TextPatternRangeEndpoint.End, selections[0], TextPatternRangeEndpoint.Start);
                string preceding = Canonical(before.GetText(-1));
                if (!document.StartsWith(preceding, StringComparison.Ordinal)) return "unsupported-selection-scope";
                insertionStart = preceding.Length;
            }
            prefix = document.Substring(0, insertionStart);
            suffix = document.Substring(insertionStart);
        }
        // Сколько знаков речи уже лежит в поле прямо перед кареткой. По этому
        // числу приложение продолжает разговор после того, как помощник был
        // перезапущен: поле при этом прикрепляется заново, участок у него
        // пустой, и без этого числа приложение либо написало бы сказанное
        // второй раз, либо сочло бы поле чужим.
        if (request.text != null && request.text.Length > 0) {
            diagnostic = "matched=" + Matched(document, insertionStart, request.text);
        }
        return Check("") ? "ready" : "unsupported-baseline";
    }
    // Matched ищет самый длинный конец поля, совпадающий с началом речи. Это и
    // есть написанное: каретка стоит сразу за ним.
    private static int Matched(string document, int caret, string text) {
        int limit = Math.Min(caret, text.Length);
        for (int length = limit; length > 0; length--) {
            int start = caret - length, i = 0;
            while (i < length && document[start + i] == text[i]) i++;
            if (i == length) return length;
        }
        return 0;
    }
    private static string Update(Request request) {
        string expected = native != IntPtr.Zero ? request.expected : Canonical(request.expected);
        string next = native != IntPtr.Zero ? request.text : Canonical(request.text);
        int mark0=0,mark1=0,mark2=0,mark3=0;
        // Одно чтение поля отвечает сразу на оба вопроса: легла ли правка и
        // лежит ли в поле то, что мы помним. Чтение длинного поля стоит около
        // четверти секунды, и лишнее чтение — это лишняя четверть секунды в
        // разговоре, который идёт своим темпом и ждать не станет.
        string current = Document();
        Trace("decide field=" + (current==null?"null":current.Length.ToString()) + " wanted=" + next.Length + " same=" + Same(current,next).ToString()); if(Same(current,next)) { seen=null; return "inserted"; }
        // Участок — то, что лежит в поле между нетронутыми краями. Края целы и
        // тогда, когда поле сошлось с памятью, и тогда, когда оно ушло вперёд
        // неё: иначе до этой строки дело бы не дошло.
        string owned = Owned(current);
        // Написанное могло дойти до поля, а подтверждение — опоздать. Тогда в
        // поле лежит ровно то, что мы написали последним: верим полю, иначе
        // повторная попытка напишет то же самое второй раз. Ждать совпадения с
        // тем, что помнит приложение, нельзя: оно помнит именно потому, что
        // подтверждения не было, и повтор за повтором копил бы сказанное дважды.
        if(pendingText!=null && Same(current,pendingText)) expected=pendingText;
        if (!Same(current,expected)) {
            // Поле разошлось с тем, что помнит приложение. Пока разговор идёт,
            // это почти всегда наша же речь, легшая мимо памяти: правка языка
            // дошла до поля и не дошла до приложения, или написанное не успело
            // подтвердиться. Раньше на этом разговор и заканчивался — приложение
            // решало, что поле правил человек, и до конца диктовки писало только
            // в буфер обмена.
            //
            // Основой берётся поле, но только если выполнены три условия. Края
            // целы: то, что было в поле до диктовки и после неё, не тронуто.
            // Участок не длиннее той речи, которую мы пишем. И поле сходится с
            // этой речью не хуже, чем сходится наша собственная память о поле.
            //
            // Последнее условие и отличает нашу недописанную правку от чужой.
            // Память приложения отстаёт законно: правка языка переписывает речь,
            // а поле получает её только следующей удачной вставкой. Поэтому
            // сравнивать поле надо не с памятью, а с речью — и требовать лишь
            // того же, чего достигла память. Чужая рука в поле отстаёт от речи
            // сильнее: слова, набранные человеком, в нашей речи не встречаются.
            int common = Common(owned, next), reached = Common(expected, next);
            // Пустой участок — это поле, прикреплённое заново: помощник был
            // перезапущен, каретка стоит сразу за написанным, и памяти
            // приложения верить здесь не в чем. Тогда мерилом остаётся память,
            // как и было до появления поля в роли основы.
            bool fresh = owned.Length == 0;
            bool ours = owned != "" || current.Length == prefix.Length + suffix.Length;
            ours = ours
                && current.StartsWith(prefix, StringComparison.Ordinal)
                && current.EndsWith(suffix, StringComparison.Ordinal)
                && owned.Length <= next.Length
                && (fresh || common >= reached);
            if (!ours) {
                // Чем именно поле не подошло: без этого отказ выглядит одинаково
                // и при чужой правке, и при своей же речи, изменившейся после
                // правки языка. Видно и общее начало, и первые знаки обеих строк.
                diagnostic = "conflict;prefix=" + prefix.Length + ";owned=" + owned.Length + ";next=" + next.Length
                    + ";memory=" + expected.Length + ";common=" + common + ";reached=" + reached
                    + ";field=" + Head(owned) + ";wanted=" + Head(next)
                    + Where(current, prefix + expected + suffix);
                return "conflict";
            }
            // Дерево доступности показывает и промежуточное состояние: пока
            // знаки ещё идут, длина успевает побывать любой. Основой становится
            // только то, что видно дважды подряд с промежутком: за промежуток
            // отставшее дерево догоняет, а настоящая недописанная правка — нет.
            if (!Settled(current)) {
                diagnostic = "unsettled;" + diagnostic;
                return "conflict";
            }
            if (owned.Length > 0) expected = owned;
            diagnostic = "adopted;" + diagnostic;
        }
        AutomationElement previous = null;
        if (native != IntPtr.Zero) {
            // Address only the pinned control. Its current caret and the active
            // application's caret do not determine the dictation destination.
            int selectionStart, selectionEnd;
            GetSelection(native, 0xb0, out selectionStart, out selectionEnd);
            int start = insertionStart, removed = expected.Length;
            string inserted = next;
            if (next.StartsWith(expected, StringComparison.Ordinal)) {
                start += expected.Length;
                removed = 0;
                inserted = next.Substring(expected.Length);
            }
            SendMessage(native, 0xb1, new IntPtr(start), new IntPtr(start + removed));
            ReplaceText(native, 0xc2, new IntPtr(1), inserted);
            // Keep an independently moved caret. The natural dictation caret
            // follows the owned span when it was already at its trailing edge.
            if (selectionStart != insertionStart + expected.Length || selectionEnd != selectionStart) {
                int delta = next.Length - expected.Length;
                if (selectionStart >= insertionStart + expected.Length) selectionStart += delta;
                if (selectionEnd >= insertionStart + expected.Length) selectionEnd += delta;
                SendMessage(native, 0xb1, new IntPtr(selectionStart), new IntPtr(selectionEnd));
            }
        } else {
            // ValuePattern.SetValue changes Chromium DOM without input events.
            // Select the pinned span through UIA and send character messages to
            // its own window so the editor receives ordinary trusted input.
            object pattern;
            if (!tracked.TryGetCurrentPattern(TextPattern.Pattern, out pattern)) return "clipboard";
            textPattern = (TextPattern)pattern;
            // Разницу ищем между полем и речью, а не между памятью приложения и
            // речью. Память отстаёт законно — правка языка переписывает речь, а
            // поле получает её следующей удачной вставкой, — и по отставшей
            // памяти разница выходила не там, где она есть: участок выделялся не
            // тот, вставка не подтверждалась, память отставала ещё сильнее. Поле
            // и есть то, что человек видит, поэтому мерило — оно.
            // Основой берётся то, что лежит в поле между краями. Пустой участок —
            // это либо поле, прикреплённое заново (наша речь уже стоит перед
            // кареткой, и писать надо только её продолжение), либо поле,
            // потерявшее нашу речь (тогда писать надо всё сказанное заново).
            // Различает их край: у прикреплённого заново он кончается тем, что мы
            // помним, а у потерявшего — нет. Пока этого различия не было, основа
            // бралась из памяти, участок искался по тексту, которого в поле нет, и
            // каждая попытка кончалась «deferred»: разговор уходил в буфер обмена
            // через тридцать четыре секунды молчания.
            string baseline = owned.Length > 0
                ? owned
                : (prefix.EndsWith(expected, StringComparison.Ordinal) ? expected : "");
            int left=0,right=0;
            while(left<Math.Min(baseline.Length,next.Length) && baseline[left]==next[left]) left++;
            while(left>0 && (!Boundary(baseline,left) || !Boundary(next,left))) left--;
            while(right<Math.Min(baseline.Length-left,next.Length-left) && baseline[baseline.Length-right-1]==next[next.Length-right-1]) right++;
            while(right>0 && (!Boundary(baseline,baseline.Length-right) || !Boundary(next,next.Length-right))) right--;
            string removed=baseline.Substring(left,baseline.Length-left-right);
            string inserted=next.Substring(left,next.Length-left-right);
            mark0=Environment.TickCount;
            TextPatternRange preceding = textPattern.DocumentRange.Clone();
            preceding.MoveEndpointByRange(TextPatternRangeEndpoint.End,preceding,TextPatternRangeEndpoint.Start);
            mark1=Environment.TickCount;
            if (!Extend(preceding,prefix+baseline.Substring(0,left))) {
                // Участок не нашёлся — это отставание дерева доступности, а не
                // пропавшее поле: приложение повторит, и следующий разговор с
                // полем начнётся с того, что в нём на самом деле лежит.
                diagnostic="preceding-missing;"+diagnostic;
                return "deferred";
            }
            TextPatternRange span=preceding.Clone();
            span.MoveEndpointByRange(TextPatternRangeEndpoint.Start,span,TextPatternRangeEndpoint.End);
            if (!Extend(span,removed)) { diagnostic="span-missing;"+diagnostic; return "deferred"; }
            mark2=Environment.TickCount;
            previous=GetForegroundWindow()==window?AutomationElement.FocusedElement:null;
            bool background=GetForegroundWindow()!=window;
            if(!background) tracked.SetFocus();
            else SendMessage(inputWindow,0x7,IntPtr.Zero,IntPtr.Zero);
            span.Select();
            bool selectedReady=false;
            int selectedLength=-1,beforeLength=-1;
            // Попытки ограничены временем, а не числом. Каждый заход читает
            // выделение, а в письме на тридцать тысяч знаков одно чтение стоит
            // полсекунды: тридцать заходов не укладывались в срок помощника, и
            // разговор кончался буфером обмена по «helper-timeout» — то есть
            // терялось поле, которое на самом деле было цело. Заодно текст
            // выделения сверяется раньше, чем то, что стоит перед ним: для
            // замены участка второго чтения не нужно вовсе.
            int selectionDeadline=Environment.TickCount+1200;
            while(Environment.TickCount<selectionDeadline) {
                TextPatternRange[] selected=textPattern.GetSelection();
                if(selected.Length==1) {
                    string selectedText=Canonical(selected[0].GetText(-1));
                    selectedLength=selectedText.Length;
                    if(removed.Length>0) {
                        if(selectedText==removed) {selectedReady=true;break;}
                    } else if(selected[0].CompareEndpoints(TextPatternRangeEndpoint.Start,selected[0],TextPatternRangeEndpoint.End)==0) {
                        TextPatternRange before=textPattern.DocumentRange.Clone();
                        before.MoveEndpointByRange(TextPatternRangeEndpoint.End,selected[0],TextPatternRangeEndpoint.Start);
                        string beforeText=Canonical(before.GetText(-1)),wantedBefore=prefix+expected.Substring(0,left);
                        beforeLength=beforeText.Length;
                        if(beforeText.TrimEnd('\r','\n')==wantedBefore.TrimEnd('\r','\n')) {selectedReady=true;break;}
                    }
                }
                Thread.Sleep(10);
            }
            // Замена на тысячи знаков — это не правка языка, а расхождение, и
            // стирать её по выделению нельзя: неточное выделение уносит с собой
            // весь хвост поля, а вернуть его будет нечем. Такой хвост
            // дописывается в конец, как и при неудавшемся выделении. Исключение
            // — поле, прикреплённое заново: там участок пуст по построению, и
            // замена всего поля и есть задуманная работа.
            // Замена, которая ничего не стирает, ничего и не теряет: проверенного
            // места хватает, а сверять с ним память тут нечего — она разошлась с
            // полем именно потому, что поле потеряло нашу речь, и требовать
            // согласия значило бы ждать его вечно.
            bool replace = selectedReady
                && (removed.Length == 0 || (Check(expected) && (owned.Length == 0 || removed.Length <= 512)));
            if (!replace) {
                // Выделить участок не вышло, а писать в поле надо: разговор не
                // ждёт. Тогда дописываем в конец поля то, чего в нём ещё нет.
                // Конец поля — единственное место, которое видно наверняка и
                // куда можно писать без выделения. Написанное раньше остаётся
                // как было: разошедшийся кусок в поле так и останется неверным,
                // зато весь дальнейший разговор ляжет следом, а не в буфер
                // обмена. Полный и верный текст всё это время лежит в истории.
                if (owned.Length<=next.Length) {
                    string tail=next.Substring(owned.Length);
                    TextPatternRange end=textPattern.DocumentRange.Clone();
                    end.MoveEndpointByRange(TextPatternRangeEndpoint.End,end,TextPatternRangeEndpoint.Start);
                    if(Extend(end,current)) {
                        end.Select();
                        if(Check(owned)) {
                            Type(inputWindow,tail);
                            if(background && GetForegroundWindow()!=window) SendMessage(inputWindow,0x8,IntPtr.Zero,IntPtr.Zero);
                            if(Confirm(owned+tail)) {
                                seen=null;
                                diagnostic="appended;from="+owned.Length+";"+diagnostic;
                                if(previous!=null && GetForegroundWindow()==window && !Automation.Compare(previous,tracked)) previous.SetFocus();
                                return "inserted";
                            }
                            // Та же двойная проверка, что и при замене текста: пустое
                            // поле значит «прочитать нельзя», а показанное начало
                            // ожидаемого — «набор дошёл, чтение отстало». И то и другое
                            // не отказ: иначе набор повторится и текст ляжет дважды.
                            string appendActual = Document();
                            string appendFull = prefix + owned + tail + suffix;
                            bool appendBlind = appendActual.Length == 0 && (owned + tail).Length > 0;
                            bool appendPartial = !appendBlind && (owned + tail).Length > 0
                                && appendActual.Length > prefix.Length
                                && appendActual.Length < appendFull.Length
                                && appendFull.StartsWith(appendActual);
                            diagnostic="append-"+(appendBlind?"unverified":appendPartial?"partial":"unconfirmed")+";"+diagnostic;
                            if ((appendBlind || appendPartial) && IsWindow(inputWindow)) {
                                seen=null;
                                return "inserted";
                            }
                            return "deferred";
                        }
                    }
                }
                if(background && GetForegroundWindow()!=window) SendMessage(inputWindow,0x8,IntPtr.Zero,IntPtr.Zero);
                diagnostic="selection-pending;selectedLength="+selectedLength+";beforeLength="+beforeLength+";wantedBeforeLength="+(prefix.Length+left);
                return "deferred";
            }
            mark3=Environment.TickCount;
            pendingExpected=expected; pendingText=next;
            Type(inputWindow,inserted);
            if(background && GetForegroundWindow()!=window) SendMessage(inputWindow,0x8,IntPtr.Zero,IntPtr.Zero);
        }
        if (pastedNow) {
            // Вставка из буфера уже сделана, и ждать её подтверждения незачем:
            // чтение поля через дерево доступности отстаёт, и ожидание стоило
            // около двухсот миллисекунд при самой вставке в двадцать одну.
            pastedNow = false;
            seen = null;
            diagnostic = "pasted;" + diagnostic;
            return "inserted";
        }
        if (Confirm(next)) {
            seen=null;
            diagnostic=diagnostic+";ms="+(mark1-mark0)+","+(mark2-mark1)+","+(mark3-mark2)+","+(Environment.TickCount-mark3);
            if(previous!=null && GetForegroundWindow()==window && !Automation.Compare(previous,tracked)) previous.SetFocus();
            return "inserted";
        }
        if(previous!=null && GetForegroundWindow()==window && !Automation.Compare(previous,tracked)) previous.SetFocus();
        // Живо ли ещё окно, в которое мы шлём знаки? Оно запоминается один раз,
        // при прикреплении к полю, а браузер пересоздаёт свои окна при
        // перерисовке — тогда знаки уходят в мёртвое окно и пропадают.
        // Поле не подтвердило текст. Но если оно при этом читается пустым, значит
        // прочитать его вообще нельзя (Chromium не отдаёт текст через UI
        // Automation), а не «текст не лёг»: мы только что его набрали. Считать это
        // отказом нельзя — служба повторит набор и уведёт разговор в буфер обмена,
        // и в поле окажутся дубли. Говорим «набрано, проверить не могу» и считаем
        // вставку сделанной: текст набран, а полный и верный он всё равно лежит в
        // истории.
        // Поле не подтвердило текст. Но это не всегда отказ:
        //
        //   * поле читается пустым — прочитать его вообще нельзя;
        //   * поле читается, но показывает НАЧАЛО ожидаемого текста — значит
        //     набор дошёл, а чтение отстало. В журнале это видно как «набрал 57
        //     знаков, прочитал 42»: проверка ловит задержку поля, а не отказ.
        //
        // В обоих случаях считать это отказом нельзя: служба повторит набор, и
        // текст ляжет второй раз — человек видит дубль и прыжок места вставки.
        string actual = Document();
        string full = prefix + next + suffix;
        bool blind = actual.Length == 0 && next.Length > 0;
        bool partial = !blind && next.Length > 0 && actual.Length > prefix.Length
            && actual.Length < full.Length && full.StartsWith(actual);
        diagnostic="tail-"+(blind?"unverified":partial?"partial":"unconfirmed")+";inputAlive="+(IsWindow(inputWindow)?"1":"0")+";inputClass="+ClassNameOf(inputWindow)+";"+diagnostic;
        if ((blind || partial) && IsWindow(inputWindow)) {
            seen=null;
            return "inserted";
        }
        return "deferred";
    }
    private const uint ClipboardUnicodeText = 13;
    // ClipboardText читает буфер обмена. Буфером владеет служба: она кладёт туда
    // расшифровку. Мы только читаем — читать чужое безопасно, в отличие от записи,
    // и без этой сверки вставлять из буфера нельзя вовсе: там могло оказаться
    // что угодно, скопированное человеком.
    private static string ClipboardText() {
        try {
            if (!IsClipboardFormatAvailable(ClipboardUnicodeText)) return null;
            if (!OpenClipboard(IntPtr.Zero)) return null;
            try {
                IntPtr handle = GetClipboardData(ClipboardUnicodeText);
                if (handle == IntPtr.Zero) return null;
                IntPtr pointer = GlobalLock(handle);
                if (pointer == IntPtr.Zero) return null;
                try { return Marshal.PtrToStringUni(pointer); }
                finally { GlobalUnlock(handle); }
            } finally { CloseClipboard(); }
        } catch { return null; }
    }
    // PasteAll посылает Ctrl+V одним вызовом. Вставку приложение выполняет как
    // одно действие — тем же путём, каким её кладёт собственный ввод приложения,
    // которым пользуется плагин. Набор по знакам приложение рисует по буквам, и
    // человек видит печатную машинку; вставка ложится целиком.
    private static bool PasteAll() {
        INPUT[] events = new INPUT[4];
        events[0].type = 1; events[0].data.keyboard.key = 0x11;
        events[1].type = 1; events[1].data.keyboard.key = 0x56;
        events[2].type = 1; events[2].data.keyboard.key = 0x56; events[2].data.keyboard.flags = 2;
        events[3].type = 1; events[3].data.keyboard.key = 0x11; events[3].data.keyboard.flags = 2;
        return SendInput((uint)events.Length, events, Marshal.SizeOf(typeof(INPUT))) == (uint)events.Length;
    }
    // SendAll — весь текст настоящими нажатиями, одним вызовом.
    //
    // Наши сообщения о знаке (WM_CHAR) — не настоящий ввод, и браузер вправе
    // отнестись к ним строже: часть их он не принимает, а разбирает по одному,
    // перерисовывая поле на каждом знаке. Отсюда и медленная вставка, и потери.
    // Настоящие нажатия он принимает как ввод человека и разбирает пачкой: на
    // живой проверке 599 знаков ушли за 842 миллисекунды, и ни один не потерялся.
    private static bool SendAll(string text) {
        if(text.Length==0) return false;
        INPUT[] events=new INPUT[text.Length*2];
        int at=0;
        foreach(char character in text) {
            ushort unit=character;
            events[at].type=1; events[at].data.keyboard=new KEYBDINPUT{key=0,scan=unit,flags=0x0004,time=0,extra=IntPtr.Zero}; at++;
            events[at].type=1; events[at].data.keyboard=new KEYBDINPUT{key=0,scan=unit,flags=0x0004|0x0002,time=0,extra=IntPtr.Zero}; at++;
        }
        return SendInput((uint)events.Length,events,Marshal.SizeOf(typeof(INPUT)))==(uint)events.Length;
    }
    private static void Type(IntPtr receiver,string inserted) {
        if(inserted.Length==0) {
            SendMessage(receiver,0x100,new IntPtr(8),new IntPtr(1));
            SendMessage(receiver,0x101,new IntPtr(8),new IntPtr(unchecked((int)0xc0000001)));
            return;
        }
        // Впереди наше окно — пишем настоящими нажатиями и одним вызовом. Не
        // вышло — пишем сообщениями, как прежде: отказ системы не должен стоить
        // человеку текста. В окно без переднего плана настоящие нажатия не
        // доедут вовсе, и там остаются сообщения.
        if(GetForegroundWindow()==window) {
            // Настоящие нажатия одним вызовом: это в десятки раз быстрее посимвольных
            // сообщений, и человек не ждёт. В окно без переднего плана они не доедут —
            // там остаются сообщения ниже.
            //
            // Шлём один раз и на сообщения в этом же заходе не откатываемся: ответ
            // «не вышло» не значит, что текст не ушёл, а откат клал его дважды.
            // От повторов текст защищён иначе — служба не просит одно и то же, а
            // «набрано, проверить не могу» не считается отказом.
            string wanted = inserted.Replace('\n','\r');
            // Сперва вставка из буфера: она ложится одним действием, а не по
            // буквам. Условие строгое — в буфере должен лежать ровно ожидаемый
            // текст; иначе остаётся прежний набор, и чужое в поле не уедет.
            if (ClipboardText() == wanted && PasteAll()) {
                pastedNow = true;
                Trace("type-paste chars=" + inserted.Length);
                return;
            }
            Trace("type-fast chars=" + inserted.Length);
            SendAll(wanted);
            return;
        }
        Trace("type-start chars=" + inserted.Length);
        foreach(char character in inserted) SendMessage(receiver,0x102,new IntPtr(character=='\n'?'\r':character),new IntPtr(1));
    }
    // Confirm ждёт, пока правка станет видна. Ждать надо тем дольше, чем больше
    // написано: поле отдаёт текст через дерево доступности, а оно отстаёт. Три
    // проверки за сто двадцать миллисекунд не успевали даже за одной фразой —
    // приложение не получало подтверждения, не запоминало написанное и на
    // следующем круге писало ту же фразу снова. Так за минуту разговора поле
    // уходило на четыре тысячи знаков вперёд памяти приложения, а разговор
    // кончался буфером обмена.
    private static bool Confirm(string wanted) {
        // Ждать дольше на длинном тексте. Браузер разбирает отданную пачку не
        // мгновенно: замер на живой проверке — 599 знаков за 842 миллисекунды,
        // значит, тысяча семьсот знаков это около двух с половиной секунд. С
        // прежним ожиданием (около двух секунд) проверка не успевала, решала, что
        // текст не лёг, и помощник набирал его снова — отсюда и размножение.
        int[] waits = wanted.Length>512 ? new int[]{0,100,250,500,1000,1500,2000,2500} : new int[]{0,30,90,240};
        foreach(int wait in waits) {
            if(wait>0) Thread.Sleep(wait);
            if (Check(wanted)) return true;
        }
        return false;
    }
    // Settled отвечает, показывало ли поле ровно это уже заметный промежуток.
    // Отставшее дерево доступности за промежуток догоняет, а недописанная правка
    // остаётся как была. Промежуток считается от первого взгляда, а не от
    // последнего: приложение повторяет попытку каждые триста миллисекунд, и с
    // прежним счётом два взгляда никогда не отстояли друг от друга на четыреста —
    // поле, потерявшее нашу речь, так и оставалось непринятым до конца разговора.
    private static bool Settled(string current) {
        if (seen != current) { seen = current; seenAt = Environment.TickCount; return false; }
        return Environment.TickCount - seenAt >= 400;
    }
    private static bool Extend(TextPatternRange range,string text) {
        if(text.Length==0) return true;
        // Конец участка ставится одним прыжком на нужную длину, а не шагом по
        // знаку: в письме на пятнадцать тысяч знаков шаг читал весь участок
        // заново на каждом знаке, и вставка не успевала за разговором.
        string trace="";
        // И здесь срок, а не число заходов: каждое чтение участка — это чтение
        // всего поля, и шестнадцать таких чтений в письме на тридцать тысяч
        // знаков длились дольше, чем приложение готово ждать помощника.
        int extendDeadline=Environment.TickCount+2500;
        for(int attempt=0;attempt<16 && Environment.TickCount<extendDeadline;attempt++) {
            string actual=Canonical(range.GetText(-1));
            if(actual==text) return true;
            int delta=text.Length-actual.Length;
            trace=trace+(attempt==0?"":",")+actual.Length+(delta>0?"+":"")+delta;
            if(delta==0) {
                // Длина сошлась, а знаки нет — дерево доступности показало чужой
                // участок той же длины. Ищем рядом, в обе стороны: правка могла
                // сдвинуться на знак-другой. Шаг по знаку короткий и только для
                // небольших участков: на пятнадцати тысячах знаков он читал бы
                // весь документ заново на каждом шаге.
                if(text.Length<=512) {
                    for(int step=0;step<32;step++) {
                        if(range.MoveEndpointByUnit(TextPatternRangeEndpoint.End,TextUnit.Character,-1)==0) break;
                        actual=Canonical(range.GetText(-1));
                        if(actual==text) return true;
                    }
                    for(int step=0;step<32;step++) {
                        if(range.MoveEndpointByUnit(TextPatternRangeEndpoint.End,TextUnit.Character,1)==0) break;
                        actual=Canonical(range.GetText(-1));
                        if(actual==text) return true;
                    }
                }
                break;
            }
            if(range.MoveEndpointByUnit(TextPatternRangeEndpoint.End,TextUnit.Character,delta)==0) break;
        }
        diagnostic="selection-scope-mismatch;want="+text.Length+";seen="+trace;
        return false;
    }
    private static bool Boundary(string text,int offset) {
        return offset==text.Length || Array.IndexOf(System.Globalization.StringInfo.ParseCombiningCharacters(text),offset)>=0;
    }
    private static int Track() {
        // Кодировку ставим на сами потоки, а не на консоль. У сборки без окна
        // (winexe) консоли нет: Console.InputEncoding бросает IOException, а
        // Console.ReadLine и Console.WriteLine без явной кодировки читают и
        // пишут в кодировке системы. Русский текст при этом приезжает
        // испорченным — и помощник набирал в поле уже кракозябры. Молчаливая
        // порча текста хуже падения, поэтому кодировка задаётся потокам явно и
        // не зависит от того, есть ли консоль.
        try {
            Console.SetIn(new System.IO.StreamReader(Console.OpenStandardInput(), new UTF8Encoding(false)));
            Console.SetOut(new System.IO.StreamWriter(Console.OpenStandardOutput(), new UTF8Encoding(false)) { AutoFlush = true });
        } catch {}
        JavaScriptSerializer serializer = new JavaScriptSerializer();
        serializer.MaxJsonLength = 1048576;
        string line;
        while ((line = Console.ReadLine()) != null) {
            string status = "clipboard";
            diagnostic = "";
            try {
                Request request = serializer.Deserialize<Request>(line);
                status = request.operation=="ping" ? "ready" : request.operation == "init" ? Initialize(request) : Update(request);
            } catch (Exception error) { diagnostic = error.GetType().Name; }
            Console.WriteLine(serializer.Serialize(new { status = status, diagnostic = diagnostic }));
        }
        return 0;
    }
    private static bool IsEditable(AutomationElement element) {
        if (element == null || !element.Current.IsEnabled || element.Current.IsPassword) return false;
        object pattern;
        if (element.TryGetCurrentPattern(ValuePattern.Pattern, out pattern)) return !((ValuePattern)pattern).Current.IsReadOnly;
        if(element.Current.ControlType==ControlType.Edit) return true;
        if(!element.TryGetCurrentPattern(TextPattern.Pattern,out pattern)) return false;
        TextPatternRange[] selection=((TextPattern)pattern).GetSelection();
        if(selection.Length==0) return false;
        object readOnly=selection[0].GetAttributeValue(TextPattern.IsReadOnlyAttribute);
        return readOnly is bool && !(bool)readOnly;
    }
    private static int Main(string[] args) {
        if (args.Length > 0 && args[0] == "--track") return Track();
        try {
            AutomationElement element = AutomationElement.FocusedElement;
            bool editable = IsEditable(element);
            Console.WriteLine(args.Length > 0 && args[0] == "--identity"
                ? (editable ? String.Join(".", element.GetRuntimeId()) : "") : (editable ? "true" : "false"));
        } catch { Console.WriteLine(args.Length > 0 ? "" : "false"); }
        return 0;
    }
}

using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Web.Script.Serialization;

// AudioCompress сжимает запись диктовки уже после распознавания: распознавателю
// нужен несжатый звук, а человеку в истории — файл, который можно послушать.
// Полчаса речи в WAV 16 кГц моно 16 бит — это 57,6 МБ, и лежать им незачем.
//
// Кодировщик берётся из самой Windows (Media Foundation): сторонних библиотек
// помощник за собой не тянет, ставить ничего не надо, прав администратора тоже.
// Он собран тем же csc.exe, что и focus-probe.exe, и едет в установщик рядом.
//
//     AudioCompress.exe <входной.wav> <выходной-путь-без-расширения> [кбит/с]
//
// В поток вывода — одна строка JSON:
//     {"path":"…","format":"mp3|m4a","bytes":123456,"seconds":10.0,"error":""}
// При отказе — {"error":"почему"} и код возврата 1.
internal static class AudioCompress
{
    // Скорость сжатой записи. 16 кГц моно 16 бит — это 256 кбит/с, поэтому
    // 24 кбит/с дают ровно то, ради чего сжатие затевалось: полчаса речи — 5,4 МБ
    // вместо 57,6 МБ, вдесятеро меньше. При 64 кбит/с запись вышла бы меньше
    // всего вчетверо, а ниже 16 кбит/с в MP3 уже шумит сама речь. Третьим
    // доводом число можно перебить: каким быть звуку в истории, решает владелец.
    private const int DefaultBitRate = 24000;
    // MF_VERSION: старшие два байта — версия SDK, младшие — версия API.
    private const int MfVersion = 0x00020070;
    // MF_SOURCE_READER_MEDIASOURCE: длительность спрашивается у самого источника,
    // а не у потока (потока в файле может и не быть — есть только звук).
    private const int SourceReaderMediaSource = unchecked((int)0xffffffff);
    private const int VariantInt64 = 20, VariantUInt64 = 21;

    private static Guid mfSinkWriterDisableThrottling = new Guid("08B845D8-2B74-4AFE-9D53-BE16D2D5AE4F");
    private static Guid mfMtMajorType = new Guid("48EBA18E-F8C9-4687-BF11-0A74C9F96A8F");
    private static Guid mfMtSubtype = new Guid("F7E34C9A-42E8-4714-B74B-CB29D72C35E5");
    private static Guid mfMtAudioNumChannels = new Guid("37E48BF5-645E-4C5B-89DE-ADA9E29B696A");
    private static Guid mfMtAudioSamplesPerSecond = new Guid("5FAEEAE7-0290-4C31-9E8A-C534F68D9DBA");
    private static Guid mfMtAudioBlockAlignment = new Guid("322DE230-9EEB-43BD-AB7A-FF412251541D");
    private static Guid mfMtAudioAvgBytesPerSecond = new Guid("1AAB75C8-CFEF-451C-AB95-AC034B8E1731");
    private static Guid mfMtAudioBitsPerSample = new Guid("F2DEB57F-40FA-4764-AA33-ED4F2D1FF669");
    private static Guid mfMtAllSamplesIndependent = new Guid("C9173739-5E56-461C-B713-46FB995CB95F");
    private static Guid mfPdDuration = new Guid("6C990D33-BB8E-477A-8598-0D5D96FCD88A");
    private static Guid mfMediaTypeAudio = new Guid("73647561-0000-0010-8000-00AA00389B71");
    private static Guid mfAudioFormatPcm = new Guid("00000001-0000-0010-8000-00AA00389B71");
    private static Guid mfAudioFormatMp3 = new Guid("00000055-0000-0010-8000-00AA00389B71");
    private static Guid mfAudioFormatAac = new Guid("00001610-0000-0010-8000-00AA00389B71");

    // Failure — отказ, о котором человеку и демону сообщает одна строка JSON.
    // Отдельным видом ошибки он потому, что отказов ожидается много и разных:
    // нет файла, не WAV, нет кодировщика. Ловить их все по месту — это try в
    // каждом вызове, а так о неудаче знает только начало программы.
    private sealed class Failure : Exception {
        public Failure(string reason) : base(reason) { }
    }

    // Wave — где в файле лежит звук и каким он записан. Смещение ищется, а не
    // берётся готовым: заголовок у записей диктовки бывает и 38, и 44 байта.
    private sealed class Wave {
        public string path;
        public int channels, rate, bits, blockAlign;
        public long offset, length;
    }

    [StructLayout(LayoutKind.Explicit)]
    private struct PropVariant {
        [FieldOffset(0)] public ushort type;
        [FieldOffset(8)] public long number;
    }

    [ComImport, Guid("2CD2D921-C447-44A7-A13C-4ADABFC247E3"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFAttributes {
        [PreserveSig] int GetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int GetItemType([In] ref Guid key, out int type);
        [PreserveSig] int CompareItem([In] ref Guid key, IntPtr value, out bool result);
        [PreserveSig] int Compare(IMFAttributes theirs, int match, out bool result);
        [PreserveSig] int GetUINT32([In] ref Guid key, out int value);
        [PreserveSig] int GetUINT64([In] ref Guid key, out long value);
        [PreserveSig] int GetDouble([In] ref Guid key, out double value);
        [PreserveSig] int GetGUID([In] ref Guid key, out Guid value);
        [PreserveSig] int GetStringLength([In] ref Guid key, out int length);
        [PreserveSig] int GetString([In] ref Guid key, [Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder value, int size, out int length);
        [PreserveSig] int GetAllocatedString([In] ref Guid key, out IntPtr value, out int length);
        [PreserveSig] int GetBlobSize([In] ref Guid key, out int size);
        [PreserveSig] int GetBlob([In] ref Guid key, [Out] byte[] value, int size, out int written);
        [PreserveSig] int GetAllocatedBlob([In] ref Guid key, out IntPtr value, out int size);
        [PreserveSig] int GetUnknown([In] ref Guid key, [In] ref Guid kind, [Out, MarshalAs(UnmanagedType.IUnknown)] out object value);
        [PreserveSig] int SetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int DeleteItem([In] ref Guid key);
        [PreserveSig] int DeleteAllItems();
        [PreserveSig] int SetUINT32([In] ref Guid key, int value);
        [PreserveSig] int SetUINT64([In] ref Guid key, long value);
        [PreserveSig] int SetDouble([In] ref Guid key, double value);
        [PreserveSig] int SetGUID([In] ref Guid key, [In] ref Guid value);
        [PreserveSig] int SetString([In] ref Guid key, [MarshalAs(UnmanagedType.LPWStr)] string value);
        [PreserveSig] int SetBlob([In] ref Guid key, byte[] value, int size);
        [PreserveSig] int SetUnknown([In] ref Guid key, [MarshalAs(UnmanagedType.IUnknown)] object value);
        [PreserveSig] int LockStore();
        [PreserveSig] int UnlockStore();
        [PreserveSig] int GetCount(out int count);
        [PreserveSig] int GetItemByIndex(int index, out Guid key, IntPtr value);
        [PreserveSig] int CopyAllItems(IMFAttributes destination);
    }

    // Поля IMFAttributes повторены в IMFMediaType и IMFSample нарочно. Сборщик
    // .NET Framework не ставит методы унаследованного COM-интерфейса после
    // родительских: у «IMFMediaType : IMFAttributes» первый же вызов уходил в
    // нулевой слот родителя и отвечал «атрибут не найден», а SetSampleTime и
    // вовсе ронял помощника. Набор, выписанный целиком, — это тот же порядок,
    // что в mfobjects.h, и он работает.
    [ComImport, Guid("44AE0FA8-EA31-4109-8D2E-4CAE4997C555"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFMediaType {
        [PreserveSig] int GetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int GetItemType([In] ref Guid key, out int type);
        [PreserveSig] int CompareItem([In] ref Guid key, IntPtr value, out bool result);
        [PreserveSig] int Compare(IntPtr theirs, int match, out bool result);
        [PreserveSig] int GetUINT32([In] ref Guid key, out int value);
        [PreserveSig] int GetUINT64([In] ref Guid key, out long value);
        [PreserveSig] int GetDouble([In] ref Guid key, out double value);
        [PreserveSig] int GetGUID([In] ref Guid key, out Guid value);
        [PreserveSig] int GetStringLength([In] ref Guid key, out int length);
        [PreserveSig] int GetString([In] ref Guid key, [Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder value, int size, out int length);
        [PreserveSig] int GetAllocatedString([In] ref Guid key, out IntPtr value, out int length);
        [PreserveSig] int GetBlobSize([In] ref Guid key, out int size);
        [PreserveSig] int GetBlob([In] ref Guid key, [Out] byte[] value, int size, out int written);
        [PreserveSig] int GetAllocatedBlob([In] ref Guid key, out IntPtr value, out int size);
        [PreserveSig] int GetUnknown([In] ref Guid key, [In] ref Guid kind, [Out, MarshalAs(UnmanagedType.IUnknown)] out object value);
        [PreserveSig] int SetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int DeleteItem([In] ref Guid key);
        [PreserveSig] int DeleteAllItems();
        [PreserveSig] int SetUINT32([In] ref Guid key, int value);
        [PreserveSig] int SetUINT64([In] ref Guid key, long value);
        [PreserveSig] int SetDouble([In] ref Guid key, double value);
        [PreserveSig] int SetGUID([In] ref Guid key, [In] ref Guid value);
        [PreserveSig] int SetString([In] ref Guid key, [MarshalAs(UnmanagedType.LPWStr)] string value);
        [PreserveSig] int SetBlob([In] ref Guid key, byte[] value, int size);
        [PreserveSig] int SetUnknown([In] ref Guid key, [MarshalAs(UnmanagedType.IUnknown)] object value);
        [PreserveSig] int LockStore();
        [PreserveSig] int UnlockStore();
        [PreserveSig] int GetCount(out int count);
        [PreserveSig] int GetItemByIndex(int index, out Guid key, IntPtr value);
        [PreserveSig] int CopyAllItems(IntPtr destination);
        [PreserveSig] int GetMajorType(out Guid major);
        [PreserveSig] int IsCompressedFormat(out bool compressed);
        [PreserveSig] int IsEqual(IMFMediaType theirs, out int flags);
        [PreserveSig] int GetRepresentation(ref Guid representation, out IntPtr value);
        [PreserveSig] int FreeRepresentation(ref Guid representation, IntPtr value);
    }

    [ComImport, Guid("045FA593-8799-42B8-BC8D-8968C6453507"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFMediaBuffer {
        [PreserveSig] int Lock(out IntPtr buffer, out int maxLength, out int currentLength);
        [PreserveSig] int Unlock();
        [PreserveSig] int GetCurrentLength(out int length);
        [PreserveSig] int SetCurrentLength(int length);
        [PreserveSig] int GetMaxLength(out int length);
    }

    [ComImport, Guid("C40A00F2-B93A-4D80-AE8C-5A1C634F58E4"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFSample {
        [PreserveSig] int GetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int GetItemType([In] ref Guid key, out int type);
        [PreserveSig] int CompareItem([In] ref Guid key, IntPtr value, out bool result);
        [PreserveSig] int Compare(IntPtr theirs, int match, out bool result);
        [PreserveSig] int GetUINT32([In] ref Guid key, out int value);
        [PreserveSig] int GetUINT64([In] ref Guid key, out long value);
        [PreserveSig] int GetDouble([In] ref Guid key, out double value);
        [PreserveSig] int GetGUID([In] ref Guid key, out Guid value);
        [PreserveSig] int GetStringLength([In] ref Guid key, out int length);
        [PreserveSig] int GetString([In] ref Guid key, [Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder value, int size, out int length);
        [PreserveSig] int GetAllocatedString([In] ref Guid key, out IntPtr value, out int length);
        [PreserveSig] int GetBlobSize([In] ref Guid key, out int size);
        [PreserveSig] int GetBlob([In] ref Guid key, [Out] byte[] value, int size, out int written);
        [PreserveSig] int GetAllocatedBlob([In] ref Guid key, out IntPtr value, out int size);
        [PreserveSig] int GetUnknown([In] ref Guid key, [In] ref Guid kind, [Out, MarshalAs(UnmanagedType.IUnknown)] out object value);
        [PreserveSig] int SetItem([In] ref Guid key, IntPtr value);
        [PreserveSig] int DeleteItem([In] ref Guid key);
        [PreserveSig] int DeleteAllItems();
        [PreserveSig] int SetUINT32([In] ref Guid key, int value);
        [PreserveSig] int SetUINT64([In] ref Guid key, long value);
        [PreserveSig] int SetDouble([In] ref Guid key, double value);
        [PreserveSig] int SetGUID([In] ref Guid key, [In] ref Guid value);
        [PreserveSig] int SetString([In] ref Guid key, [MarshalAs(UnmanagedType.LPWStr)] string value);
        [PreserveSig] int SetBlob([In] ref Guid key, byte[] value, int size);
        [PreserveSig] int SetUnknown([In] ref Guid key, [MarshalAs(UnmanagedType.IUnknown)] object value);
        [PreserveSig] int LockStore();
        [PreserveSig] int UnlockStore();
        [PreserveSig] int GetCount(out int count);
        [PreserveSig] int GetItemByIndex(int index, out Guid key, IntPtr value);
        [PreserveSig] int CopyAllItems(IntPtr destination);
        [PreserveSig] int GetSampleFlags(out int flags);
        [PreserveSig] int SetSampleFlags(int flags);
        [PreserveSig] int GetSampleTime(out long time);
        [PreserveSig] int SetSampleTime(long time);
        [PreserveSig] int GetSampleDuration(out long duration);
        [PreserveSig] int SetSampleDuration(long duration);
        [PreserveSig] int GetBufferCount(out int count);
        [PreserveSig] int GetBufferByIndex(int index, [MarshalAs(UnmanagedType.Interface)] out IMFMediaBuffer buffer);
        [PreserveSig] int ConvertToContiguousBuffer([MarshalAs(UnmanagedType.Interface)] out IMFMediaBuffer buffer);
        [PreserveSig] int AddBuffer(IMFMediaBuffer buffer);
        [PreserveSig] int RemoveBufferByIndex(int index);
        [PreserveSig] int RemoveAllBuffers();
        [PreserveSig] int GetTotalLength(out int length);
        [PreserveSig] int CopyToBuffer(IMFMediaBuffer buffer);
    }

    [ComImport, Guid("3137F1CD-FE5E-4805-A5D8-FB477448CB3D"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFSinkWriter {
        [PreserveSig] int AddStream(IMFMediaType target, out int stream);
        [PreserveSig] int SetInputMediaType(int stream, IMFMediaType input, IMFAttributes parameters);
        [PreserveSig] int BeginWriting();
        [PreserveSig] int WriteSample(int stream, IMFSample sample);
        [PreserveSig] int SendStreamTick(int stream, long time);
        [PreserveSig] int PlaceMarker(int stream, IntPtr context);
        [PreserveSig] int NotifyEndOfSegment(int stream);
        [PreserveSig] int Flush(int stream);
        // Слот зовётся Finalize; в C# это имя занято сборщиком мусора, поэтому
        // метод назван иначе — важен порядок слотов, а не имена.
        [PreserveSig] int FinalizeWriter();
        [PreserveSig] int GetServiceForStream(int stream, [In] ref Guid service, [In] ref Guid kind, out IntPtr value);
        [PreserveSig] int GetStatistics(int stream, IntPtr statistics);
    }

    [ComImport, Guid("70AE66F2-C809-4E4F-8915-BDCB406B7993"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IMFSourceReader {
        [PreserveSig] int GetStreamSelection(int stream, out bool selected);
        [PreserveSig] int SetStreamSelection(int stream, bool selected);
        [PreserveSig] int GetNativeMediaType(int stream, int index, [MarshalAs(UnmanagedType.Interface)] out IMFMediaType type);
        [PreserveSig] int GetCurrentMediaType(int stream, [MarshalAs(UnmanagedType.Interface)] out IMFMediaType type);
        [PreserveSig] int SetCurrentMediaType(int stream, IntPtr reserved, IMFMediaType type);
        [PreserveSig] int SetCurrentPosition([In] ref Guid format, IntPtr position);
        [PreserveSig] int ReadSample(int stream, int flags, out int actual, out int streamFlags, out long time, [MarshalAs(UnmanagedType.Interface)] out IMFSample sample);
        [PreserveSig] int Flush(int stream);
        [PreserveSig] int GetServiceForStream(int stream, [In] ref Guid service, [In] ref Guid kind, out IntPtr value);
        [PreserveSig] int GetPresentationAttribute(int stream, [In] ref Guid attribute, ref PropVariant value);
    }

    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFStartup(int version, int flags);
    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFShutdown();
    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFCreateAttributes([Out, MarshalAs(UnmanagedType.Interface)] out IMFAttributes attributes, int size);
    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFCreateMediaType([Out, MarshalAs(UnmanagedType.Interface)] out IMFMediaType type);
    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFCreateSample([Out, MarshalAs(UnmanagedType.Interface)] out IMFSample sample);
    [DllImport("mfplat.dll", ExactSpelling = true)]
    private static extern int MFCreateMemoryBuffer(int size, [Out, MarshalAs(UnmanagedType.Interface)] out IMFMediaBuffer buffer);
    [DllImport("mfreadwrite.dll", ExactSpelling = true)]
    private static extern int MFCreateSinkWriterFromURL([MarshalAs(UnmanagedType.LPWStr)] string url, IntPtr stream, IMFAttributes settings, [Out, MarshalAs(UnmanagedType.Interface)] out IMFSinkWriter writer);
    [DllImport("mfreadwrite.dll", ExactSpelling = true)]
    private static extern int MFCreateSourceReaderFromURL([MarshalAs(UnmanagedType.LPWStr)] string url, IMFAttributes settings, [Out, MarshalAs(UnmanagedType.Interface)] out IMFSourceReader reader);
    [DllImport("ole32.dll", ExactSpelling = true)]
    private static extern int CoInitializeEx(IntPtr reserved, int mode);
    [DllImport("ole32.dll", ExactSpelling = true)]
    private static extern void CoUninitialize();
    [DllImport("ole32.dll", ExactSpelling = true)]
    private static extern int PropVariantClear(ref PropVariant value);

    private static int Main(string[] args) {
        // Кодировку ставим потоку вывода, а не консоли: у сборки без окна
        // (winexe) консоли нет, Console.OutputEncoding бросает IOException, а
        // Console.WriteLine без явной кодировки пишет в кодировке системы —
        // русские пути в ответе приезжали бы испорченными. Вход не трогаем: там
        // звук, а не текст.
        try {
            Console.SetOut(new System.IO.StreamWriter(Console.OpenStandardOutput(), new UTF8Encoding(false)) { AutoFlush = true });
        } catch {}
        // Потоковый режим разбирается первым: у него свои ключи, и проверка
        // «два или три довода» к нему не подходит.
        if (args.Length >= 2 && args[0] == "--stream") return StreamMain(args);
        if (args.Length < 2 || args.Length > 3)
            return Fail("usage: нужен входной WAV и выходной путь без расширения");
        int bitRate = DefaultBitRate;
        if (args.Length == 3 && (!Int32.TryParse(args[2], out bitRate) || bitRate < 8000 || bitRate > 320000))
            return Fail("usage: непонятная скорость " + args[2]);
        if (!File.Exists(args[0]))
            return Fail("no-input: нет входного файла " + args[0]);
        // Апартамент размечен руками: помощник консольный, и полагаться на то,
        // что COM поднял кто-то до нас, не на что. S_FALSE — это «уже поднят»,
        // а не отказ, поэтому отказом считается только отрицательный ответ.
        bool com = CoInitializeEx(IntPtr.Zero, 0) >= 0;
        try {
            Check(MFStartup(MfVersion, 0), "mf-startup");
            try {
                Wave wave = ReadWave(args[0]);
                double source = wave.length / (double)(wave.rate * wave.blockAlign);
                string path, format, refused;
                Compress(wave, args[1], bitRate, out path, out format, out refused);
                long bytes = new FileInfo(path).Length;
                double seconds = Duration(path);
                // Файл, который Windows не берётся прочитать обратно, — это мусор,
                // и объявлять сжатие удавшимся нельзя: демон по нашему ответу
                // удаляет WAV, а он единственная запись разговора.
                if (seconds <= 0 || Math.Abs(seconds - source) > Math.Max(1, source / 10))
                    throw new Failure("verify: " + path + " читается как " + seconds.ToString("0.##") + " с вместо " + source.ToString("0.##") + " с");
                Console.Error.WriteLine("audio-compress: " + format + ", " + bytes + " байт, " + seconds.ToString("0.##") + " с из " + source.ToString("0.##") + " с");
                Console.WriteLine(new JavaScriptSerializer().Serialize(new { path = path, format = format, bytes = bytes, seconds = Math.Round(seconds, 2), error = "" }));
                return 0;
            } finally { MFShutdown(); }
        } catch (Failure error) {
            return Fail(error.Message);
        } catch (Exception error) {
            return Fail("crash: " + error.GetType().Name + ": " + error.Message);
        } finally {
            if (com) CoUninitialize();
        }
    }

    // Compress пробует кодировщики по очереди. MP3 первым: о нём просил владелец,
    // и играется он везде. Но в Windows без Media Feature Pack кодировщика MP3
    // нет вовсе, и тогда запись важнее формата — берётся AAC в контейнере .m4a,
    // который есть всегда. Что вышло на самом деле, видно в поле format: промолчать
    // об откате нельзя, иначе владелец будет искать в истории .mp3, которого нет.
    private static void Compress(Wave wave, string target, int bitRate, out string path, out string format, out string refused) {
        string mp3, m4a;
        refused = "";
        path = target + ".mp3";
        format = "mp3";
        if (TryEncode(wave, path, false, bitRate, out mp3)) return;
        Console.Error.WriteLine("audio-compress: mp3 не вышел (" + mp3 + "), берём m4a");
        path = target + ".m4a";
        format = "m4a";
        if (TryEncode(wave, path, true, bitRate, out m4a)) return;
        refused = "encode: mp3 — " + mp3 + "; m4a — " + m4a;
        throw new Failure(refused);
    }

    private static bool TryEncode(Wave wave, string path, bool aac, int bitRate, out string refused) {
        refused = "";
        try {
            // Готовый файл кодировщик не переписывает, а дописывает: остаток
            // прошлой попытки уехал бы в историю вместе с новой записью.
            TryDelete(path);
            WriteMedia(wave, path, aac, bitRate);
            return true;
        } catch (Failure error) {
            refused = error.Message;
            TryDelete(path);
            return false;
        }
    }

    private static void WriteMedia(Wave wave, string path, bool aac, int bitRate) {
        WriteMedia(wave, path, aac, bitRate, null);
    }

    // feed зовётся вместо чтения из файла: кодировщик тот же, меняется только то,
    // откуда берётся звук. Пусто — значит из WAV, как было.
    private static void WriteMedia(Wave wave, string path, bool aac, int bitRate, Action<IMFSinkWriter, int> feed) {
        IMFSinkWriter writer = null;
        IMFAttributes settings = null;
        IMFMediaType output = null, input = null;
        try {
            Check(MFCreateAttributes(out settings, 1), "mf-attributes");
            // Кодировщик догоняет запись своим темпом, а не темпом часов: без
            // этого он спит между кусками, и полчаса речи кодировались бы
            // минутами — ровно то, чего помощнику делать нельзя.
            Check(settings.SetUINT32(ref mfSinkWriterDisableThrottling, 1), "mf-throttling");
            Check(MFCreateSinkWriterFromURL(path, IntPtr.Zero, settings, out writer), "mf-writer");
            Guid subtype = aac ? mfAudioFormatAac : mfAudioFormatMp3;
            Check(MFCreateMediaType(out output), "mf-output-type");
            Check(output.SetGUID(ref mfMtMajorType, ref mfMediaTypeAudio), "mf-output-major");
            Check(output.SetGUID(ref mfMtSubtype, ref subtype), "mf-output-subtype");
            Check(output.SetUINT32(ref mfMtAudioNumChannels, wave.channels), "mf-output-channels");
            Check(output.SetUINT32(ref mfMtAudioSamplesPerSecond, wave.rate), "mf-output-rate");
            Check(output.SetUINT32(ref mfMtAudioAvgBytesPerSecond, bitRate / 8), "mf-output-bitrate");
            if (aac) Check(output.SetUINT32(ref mfMtAudioBitsPerSample, wave.bits), "mf-output-bits");
            int stream;
            Check(writer.AddStream(output, out stream), "mf-add-stream");
            Check(MFCreateMediaType(out input), "mf-input-type");
            Check(input.SetGUID(ref mfMtMajorType, ref mfMediaTypeAudio), "mf-input-major");
            Check(input.SetGUID(ref mfMtSubtype, ref mfAudioFormatPcm), "mf-input-subtype");
            Check(input.SetUINT32(ref mfMtAudioNumChannels, wave.channels), "mf-input-channels");
            Check(input.SetUINT32(ref mfMtAudioSamplesPerSecond, wave.rate), "mf-input-rate");
            Check(input.SetUINT32(ref mfMtAudioBitsPerSample, wave.bits), "mf-input-bits");
            Check(input.SetUINT32(ref mfMtAudioBlockAlignment, wave.blockAlign), "mf-input-align");
            Check(input.SetUINT32(ref mfMtAudioAvgBytesPerSecond, wave.rate * wave.blockAlign), "mf-input-bytes");
            Check(input.SetUINT32(ref mfMtAllSamplesIndependent, 1), "mf-input-independent");
            Check(writer.SetInputMediaType(stream, input, null), "mf-set-input");
            Check(writer.BeginWriting(), "mf-begin");
            if (feed == null) WriteSamples(writer, stream, wave); else feed(writer, stream);
            Check(writer.FinalizeWriter(), "mf-finalize");
        } finally {
            Release(input);
            Release(output);
            Release(settings);
            Release(writer);
        }
    }

    // Потоковый режим: звук читается со входа и кодируется по ходу речи, поэтому
    // к моменту остановки файл уже готов и отдельного этапа упаковки не нужно.
    //
    //     AudioCompress.exe --stream --out <путь-без-расширения> [--rate 16000] [--kbps 24000]
    //
    // На вход идёт сырой PCM: 16 бит, моно, младшим байтом вперёд — ровно то, что
    // записывает диктовка. Ответ — та же строка JSON, что и у файлового режима,
    // чтобы зовущий не разбирал два разных ответа.
    private static int StreamMain(string[] args) {
        string outPath = null;
        int rate = 16000, bitRate = DefaultBitRate;
        // Кодировщик выбирает зовущий, а не константа: mp3 тут стоял зашитым, и
        // сменить его было нечем. AAC даёт тот же порядок размера и лежит в
        // Media Foundation рядом с mp3 — чужой библиотеки для него не нужно.
        bool aac = false;
        for (int i = 1; i < args.Length - 1; i++) {
            if (args[i] == "--out") outPath = args[i + 1];
            else if (args[i] == "--rate") Int32.TryParse(args[i + 1], out rate);
            else if (args[i] == "--kbps") Int32.TryParse(args[i + 1], out bitRate);
            else if (args[i] == "--codec") aac = args[i + 1] == "aac";
        }
        if (String.IsNullOrEmpty(outPath)) return StreamRefuse("stream: не назван --out");
        if (rate < 8000 || rate > 48000) return StreamRefuse("stream: частота " + rate + " не годится");
        if (bitRate < 8000) bitRate = DefaultBitRate;
        Wave wave = new Wave { path = "-", channels = 1, rate = rate, bits = 16, blockAlign = 2 };
        // Путь кодировщику отдаётся вместе с расширением: по нему Media
        // Foundation выбирает формат файла.
        string made = outPath + (aac ? ".m4a" : ".mp3");
        long fed = 0;
        // Media Foundation поднимается здесь же: у файлового пути это делает Main,
        // а потоковый возвращается раньше него. Без запуска движка создание
        // кодировщика отказывает (0xC00D3E85), и это выглядит как ошибка в пути,
        // а не как незапущенный движок — на что я и потратил один заход.
        CoInitializeEx(IntPtr.Zero, 0);
        try {
            Check(MFStartup(MfVersion, 0), "mf-startup");
            try {
                TryDelete(made);
                // Путь кодировщику отдаётся вместе с расширением: по нему Media
                // Foundation выбирает формат файла.
                WriteMedia(wave, made, aac, bitRate, delegate(IMFSinkWriter writer, int stream) {
                    fed = FeedStream(writer, stream, wave);
                });
            } catch (Failure error) {
                return StreamRefuse(error.Message);
            }
        } finally {
            MFShutdown();
        }
        long bytes = 0;
        try { bytes = new FileInfo(made).Length; } catch { bytes = 0; }
        double seconds = Math.Round((double)fed / (wave.rate * wave.blockAlign), 2);
        Console.WriteLine(new JavaScriptSerializer().Serialize(
            new { path = made, format = aac ? "aac" : "mp3", bytes = bytes, seconds = seconds, error = "" }));
        return 0;
    }

    private static int StreamRefuse(string reason) {
        Console.WriteLine(new JavaScriptSerializer().Serialize(new { error = reason }));
        return 1;
    }

    // FeedStream кормит кодировщик по секунде, как только звук появляется на
    // входе. Последний неполный кусок тоже уходит: иначе хвост речи пропал бы.
    private static long FeedStream(IMFSinkWriter writer, int stream, Wave wave) {
        Stream source = Console.OpenStandardInput();
        int block = wave.rate * wave.blockAlign;
        byte[] buffer = new byte[block];
        long done = 0;
        while (true) {
            int got = 0;
            while (got < block) {
                int read = source.Read(buffer, got, block - got);
                if (read <= 0) break;
                got += read;
            }
            if (got <= 0) break;
            WriteChunk(writer, stream, buffer, got, wave, done);
            done += got;
            if (got < block) break;
        }
        return done;
    }

    // WriteSamples кормит кодировщик секундными кусками. Полчаса речи — это 1800
    // кусков по 32 КБ: кодирование идёт быстрее самой записи, а память занимает
    // один кусок, а не весь час разговора.
    private static void WriteSamples(IMFSinkWriter writer, int stream, Wave wave) {
        byte[] chunk = new byte[wave.rate * wave.blockAlign];
        long left = wave.length, done = 0;
        using (FileStream file = File.OpenRead(wave.path)) {
            file.Seek(wave.offset, SeekOrigin.Begin);
            while (left > 0) {
                int size = (int)Math.Min(chunk.Length, left), read = 0;
                while (read < size) {
                    int got = file.Read(chunk, read, size - read);
                    if (got <= 0) break;
                    read += got;
                }
                if (read == 0) break;
                WriteChunk(writer, stream, chunk, read, wave, done);
                done += read;
                left -= read;
            }
        }
    }

    private static void WriteChunk(IMFSinkWriter writer, int stream, byte[] data, int size, Wave wave, long done) {
        IMFSample sample = null;
        IMFMediaBuffer buffer = null;
        try {
            Check(MFCreateSample(out sample), "mf-sample");
            Check(MFCreateMemoryBuffer(size, out buffer), "mf-buffer");
            IntPtr target;
            int maximum, current;
            Check(buffer.Lock(out target, out maximum, out current), "mf-lock");
            try {
                Marshal.Copy(data, 0, target, size);
            } finally {
                Check(buffer.Unlock(), "mf-unlock");
            }
            Check(buffer.SetCurrentLength(size), "mf-length");
            Check(sample.AddBuffer(buffer), "mf-add-buffer");
            // Время считается по байтам, а не по часам: секунда звука — это
            // ровно rate*blockAlign байт, и ни одного пропуска в записи быть не
            // должно, иначе звук в истории поедет и разойдётся с текстом.
            long units = wave.rate * (long)wave.blockAlign;
            Check(sample.SetSampleTime(done * 10000000L / units), "mf-time");
            Check(sample.SetSampleDuration(size * 10000000L / units), "mf-duration");
            Check(writer.WriteSample(stream, sample), "mf-write");
        } finally {
            Release(buffer);
            Release(sample);
        }
    }

    // Duration читает длительность обратно средствами Windows: только так видно,
    // что на диске лежит запись, а не её заголовок. Один и тот же вызов отвечает
    // и на вопрос «файл открывается», и на вопрос «сколько в нём звука».
    private static double Duration(string path) {
        IMFSourceReader reader = null;
        try {
            Check(MFCreateSourceReaderFromURL(path, null, out reader), "mf-reader");
            PropVariant value = new PropVariant();
            Check(reader.GetPresentationAttribute(SourceReaderMediaSource, ref mfPdDuration, ref value), "mf-duration");
            try {
                if (value.type != VariantUInt64 && value.type != VariantInt64)
                    throw new Failure("verify: у " + path + " нет длительности");
                return value.number / 10000000.0;
            } finally {
                PropVariantClear(ref value);
            }
        } finally {
            Release(reader);
        }
    }

    // ReadWave ищет в файле куски RIFF, а не отсчитывает заголовок по смещению
    // 44: у записей диктовки заголовок бывает и 38 байт, и взятое на глаз
    // смещение скормило бы кодировщику два байта заголовка — запись начиналась
    // бы со щелчка, а хвост файла терялся бы.
    private static Wave ReadWave(string path) {
        using (FileStream file = File.OpenRead(path)) {
            byte[] head = new byte[12];
            if (file.Read(head, 0, 12) != 12 || Tag(head, 0) != "RIFF" || Tag(head, 8) != "WAVE")
                throw new Failure("not-wave: не разобрать заголовок WAV в " + path);
            Wave wave = new Wave { path = path };
            byte[] chunk = new byte[8];
            while (file.Position + 8 <= file.Length) {
                if (file.Read(chunk, 0, 8) != 8) break;
                string name = Tag(chunk, 0);
                long start = file.Position;
                long size = BitConverter.ToUInt32(chunk, 4);
                // Нулевая длина — это запись, оборванная на середине: заголовок
                // не успел получить размер, а звук на диске лежит весь.
                if (size <= 0 || start + size > file.Length) size = file.Length - start;
                if (name == "fmt ") {
                    byte[] format = new byte[40];
                    int want = (int)Math.Min(format.Length, size), got = 0;
                    while (got < want) {
                        int read = file.Read(format, got, want - got);
                        if (read <= 0) break;
                        got += read;
                    }
                    if (got < 16) throw new Failure("not-wave: короткий fmt в " + path);
                    int kind = BitConverter.ToUInt16(format, 0);
                    // WAVE_FORMAT_EXTENSIBLE — это тот же PCM, только вид записи
                    // назван не числом, а образцом в конце куска.
                    if (kind == 0xfffe && got >= 40) kind = BitConverter.ToUInt16(format, 24);
                    wave.channels = BitConverter.ToUInt16(format, 2);
                    wave.rate = (int)BitConverter.ToUInt32(format, 4);
                    wave.blockAlign = BitConverter.ToUInt16(format, 12);
                    wave.bits = BitConverter.ToUInt16(format, 14);
                    if (kind != 1) throw new Failure("not-pcm: звук в " + path + " не PCM, а " + kind);
                    if (wave.bits != 16 || wave.channels < 1 || wave.rate <= 0 || wave.blockAlign != wave.channels * 2)
                        throw new Failure("not-pcm: " + wave.channels + " канала, " + wave.rate + " Гц, " + wave.bits + " бит — столько помощник не кодирует");
                } else if (name == "data") {
                    wave.offset = start;
                    wave.length = size - size % wave.blockAlign;
                    break;
                }
                // Куски выравниваются на чётную длину, и лишний байт заполнителя
                // считается частью куска, а не началом следующего.
                file.Seek(start + size + (size & 1), SeekOrigin.Begin);
            }
            if (wave.offset == 0 || wave.length <= 0)
                throw new Failure("not-wave: в " + path + " нет куска data со звуком");
            return wave;
        }
    }

    private static string Tag(byte[] buffer, int offset) {
        return Encoding.ASCII.GetString(buffer, offset, 4);
    }

    // Check превращает код отказа Media Foundation в понятную строку. Код
    // оставлен как есть: по нему отказ ищется в списке ошибок Windows, а без
    // него «не вышло» не отличить от «не вышло по другой причине».
    private static void Check(int result, string what) {
        if (result < 0) throw new Failure(what + " 0x" + result.ToString("X8"));
    }

    private static void Release(object instance) {
        if (instance != null && Marshal.IsComObject(instance)) Marshal.ReleaseComObject(instance);
    }

    private static void TryDelete(string path) {
        try { File.Delete(path); } catch { }
    }

    private static int Fail(string reason) {
        Console.WriteLine(new JavaScriptSerializer().Serialize(new { error = reason }));
        return 1;
    }
}

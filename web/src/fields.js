// 编辑面板可配置显示的全部字段（29 元素）。editable=true 可编辑写标签；
// false 只读展示（本地探测值/元信息）。
export const FIELDS = [
  { id: 'cover', label: 'bCover', editable: false },
  { id: 'title', label: 'bTitle', editable: true },
  { id: 'artist', label: 'bArtist', editable: true },
  { id: 'album', label: 'bAlbum', editable: true },
  { id: 'albumartist', label: 'bAlbumArtist', editable: true },
  { id: 'genre', label: 'bGenre', editable: true },
  { id: 'year', label: 'bYear', editable: true },
  { id: 'tracknumber', label: 'bTrack', editable: true },
  { id: 'tracktotal', label: 'fTrackTotal', editable: true },
  { id: 'composer', label: 'fComposer', editable: true },
  { id: 'lyricist', label: 'fLyricist', editable: true },
  { id: 'language', label: 'fLanguage', editable: true },
  { id: 'albumtype', label: 'fAlbumType', editable: true },
  { id: 'comment', label: 'fComment', editable: true },
  { id: 'discnumber', label: 'fDisc', editable: true },
  { id: 'disctotal', label: 'fDiscTotal', editable: true },
  { id: 'lyrics', label: 'bLyrics', editable: true },
  { id: 'filename', label: 'fFileName', editable: false },
  { id: 'lrcfile', label: 'fLrcFile', editable: false },
  { id: 'bitrate', label: 'fBitrate', editable: false },
  { id: 'samplerate', label: 'fSampleRate', editable: false },
  { id: 'bitdepth', label: 'fBitDepth', editable: false },
  { id: 'channels', label: 'fChannels', editable: false },
  { id: 'duration', label: 'fDuration', editable: false },
  { id: 'filesize', label: 'fFileSize', editable: false },
  { id: 'sha256', label: 'fSha256', editable: false },
  { id: 'path', label: 'fPath', editable: false },
  { id: 'mbtrack', label: 'fMbTrack', editable: false },
  { id: 'mbalbum', label: 'fMbAlbum', editable: false },
]

// 默认勾选的常用字段（未在设置页自定义时）
export const DEFAULT_EDIT_FIELDS = [
  'cover', 'title', 'artist', 'album', 'albumartist', 'genre',
  'year', 'tracknumber', 'tracktotal', 'composer', 'lyricist', 'lyrics',
]

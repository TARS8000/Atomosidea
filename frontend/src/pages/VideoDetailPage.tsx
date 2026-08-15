import { useState, useEffect, useRef } from 'react';
import { useParams, useNavigate, Link as RouterLink, useLocation } from 'react-router-dom';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import {
  Container,
  Typography,
  Box,
  CircularProgress,
  Alert,
  Button,
  IconButton,
  Tooltip,
  FormControl,
  Select,
  MenuItem,
  SelectChangeEvent,
  Slider,
  styled,
} from '@mui/material';
import EditIcon from '@mui/icons-material/Edit';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import PauseIcon from '@mui/icons-material/Pause';
import VolumeUpIcon from '@mui/icons-material/VolumeUp';
import VolumeOffIcon from '@mui/icons-material/VolumeOff';
import FullscreenIcon from '@mui/icons-material/Fullscreen';
import FullscreenExitIcon from '@mui/icons-material/FullscreenExit';
import Hls from 'hls.js';

// --- Styled Components for Custom Controls ---
const ControlBar = styled(Box)(({ theme }) => ({
  position: 'absolute',
  bottom: 0,
  left: 0,
  right: 0,
  display: 'flex',
  alignItems: 'center',
  padding: theme.spacing(1, 2),
  background: 'rgba(0, 0, 0, 0.6)',
  transition: 'opacity 0.3s',
}));

const TimeText = styled(Typography)({
  color: 'white',
  minWidth: '50px',
  textAlign: 'center',
});

interface Video {
  id: string;
  title: string;
  description: string;
  filename: string;
  uploader_id: string;
  created_at: string;
  status: string;
  processing_details: string | null;
}

interface Uploader {
  username: string;
}

const formatTime = (timeInSeconds: number) => {
  const minutes = Math.floor(timeInSeconds / 60);
  const seconds = Math.floor(timeInSeconds % 60);
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
};

const VideoDetailPage = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const initialStatus = (location.state as { initialStatus?: string })?.initialStatus || 'unknown';

  const [video, setVideo] = useState<Video | null>(null);
  const [uploader, setUploader] = useState<Uploader | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [currentStatus, setCurrentStatus] = useState<string>(initialStatus);
  const { user, token } = useAuth();
  const navigate = useNavigate();

  const videoContainerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);

  // Player State
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [volume, setVolume] = useState(1);
  const [isMuted, setIsMuted] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [controlsVisible, setControlsVisible] = useState(true);

  // HLS Quality State
  const [availableQualities, setAvailableQualities] = useState<{ level: number, label: string }[]>([]);
  const [currentQuality, setCurrentQuality] = useState<number | 'auto'>('auto');
  const [activeQualityLabel, setActiveQualityLabel] = useState('');

  // --- [1] 動画と投稿者情報の取得 ---
  useEffect(() => {
    // ID が未確定の場合は API リクエストを発行しない (404防止)
    if (!id) return;

    let pollingInterval: number | null = null;

    const stopPolling = () => {
      if (pollingInterval) {
        clearInterval(pollingInterval);
        pollingInterval = null;
      }
    };

    const fetchDetails = async () => {
      try {
        const videoApiUrl = `/api/videos/${id}`;
        const videoRes = await axios.get(videoApiUrl);

        setVideo(videoRes.data);
        setCurrentStatus(videoRes.data.status);

        if (['public', 'error', 'invalid'].includes(videoRes.data.status)) {
          setLoading(false);
          stopPolling();
        } else {
          setLoading(true);
        }

        const uploaderId = videoRes.data?.uploader_id;
        if (uploaderId != null) {
          try {
            const profileApiUrl = `/api/profile/${uploaderId}`;
            const uploaderRes = await axios.get(profileApiUrl);
            setUploader(uploaderRes.data);
          } catch (e) {
            console.error('Failed to fetch uploader details:', e);
          }
        }

      } catch (err) {
        console.error('Failed to fetch video details:', err);
        setError('詳細の読み込みに失敗しました。');
        setLoading(false);
        stopPolling();
      }
    };

    fetchDetails();

    pollingInterval = window.setInterval(() => {
      if (currentStatus !== 'public' && currentStatus !== 'error' && currentStatus !== 'invalid') {
        fetchDetails();
      }
    }, 5000);

    return () => {
      stopPolling();
    };
  }, [id]);

  // --- [2] HLS.js のセットアップ ---
  useEffect(() => {
    if (loading || !video || video.status !== 'public' || !videoRef.current) return;

    // 💡 すでに HLS インスタンスが存在する場合は再初期化しない（再レンダリング対策）
    if (hlsRef.current) return;

    const videoEl = videoRef.current;
    const streamUrl = `/api/videos/${video.id}/stream/playlist.m3u8`;

    if (Hls.isSupported()) {
      const hls = new Hls({ debug: true });
      hlsRef.current = hls;

      hls.on(Hls.Events.MANIFEST_PARSED, (_event, data) => {
        const qualities = data.levels.map((level, index) => ({
          level: index,
          label: level.height ? `${level.height}p` : `${Math.round(level.bitrate / 1000)}kbps`,
        }));
        setAvailableQualities([{ level: -1, label: '自動' }, ...qualities]);
        setCurrentQuality('auto');
      });

      hls.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => {
        const currentLevel = hls.levels[data.level];
        if (currentLevel) {
          const label = currentLevel.height ? `${currentLevel.height}p` : `${Math.round(currentLevel.bitrate / 1000)}kbps`;
          setActiveQualityLabel(`(${label})`);
        }
      });

      hls.on(Hls.Events.ERROR, (_event, data) => {
        if (data.fatal) {
          switch (data.type) {
            case Hls.ErrorTypes.NETWORK_ERROR: hls.startLoad(); break;
            case Hls.ErrorTypes.MEDIA_ERROR: hls.recoverMediaError(); break;
            default:
              hls.destroy();
              hlsRef.current = null;
              break;
          }
        }
      });

      hls.loadSource(streamUrl);
      hls.attachMedia(videoEl);

      return () => {
        if (hlsRef.current) {
          hlsRef.current.destroy();
          hlsRef.current = null;
        }
      };
    } else if (videoEl.canPlayType('application/vnd.apple.mpegurl')) {
      videoEl.src = streamUrl;
    } else {
      setError('お使いのブラウザはHLS動画の再生に対応していません。');
    }
  }, [loading, video?.id, video?.status]);

  // --- [3] フルスクリーンハンドリング ---
  useEffect(() => {
    const handleFullscreenChange = () => {
      setIsFullscreen(!!document.fullscreenElement);
    };
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    return () => document.removeEventListener('fullscreenchange', handleFullscreenChange);
  }, []);

  // --- Player Control Handlers ---
  const togglePlayPause = () => {
    if (videoRef.current) {
      isPlaying ? videoRef.current.pause() : videoRef.current.play();
      setIsPlaying(!isPlaying);
    }
  };

  const handleSeek = (_event: Event, newValue: number | number[]) => {
    if (videoRef.current) {
      videoRef.current.currentTime = newValue as number;
      setCurrentTime(newValue as number);
    }
  };

  const handleVolumeChange = (_event: Event, newValue: number | number[]) => {
    if (videoRef.current) {
      const newVolume = newValue as number;
      videoRef.current.volume = newVolume;
      setVolume(newVolume);
      setIsMuted(newVolume === 0);
    }
  };

  const toggleMute = () => {
    if (videoRef.current) {
      const newMuted = !isMuted;
      videoRef.current.muted = newMuted;
      if (!newMuted && volume === 0) {
        setVolume(0.5);
        videoRef.current.volume = 0.5;
      }
    }
  };

  const toggleFullscreen = () => {
    if (!videoContainerRef.current) return;
    if (!isFullscreen) {
      videoContainerRef.current.requestFullscreen();
    } else {
      document.exitFullscreen();
    }
  };

  const handleQualityChange = (event: SelectChangeEvent<number | 'auto'>) => {
    const selectedQuality = event.target.value as number | 'auto';
    setCurrentQuality(selectedQuality);
    if (hlsRef.current) {
      hlsRef.current.currentLevel = selectedQuality === 'auto' ? -1 : (selectedQuality as number);
    }
  };

  const handleDelete = async () => {
    if (window.confirm('本当にこの動画を削除しますか？')) {
      try {
        await axios.delete(`/api/videos/delete/${id}`, { headers: { Authorization: `Bearer ${token}` } });
        alert('動画を削除しました。');
        navigate('/');
      } catch (err) {
        setError('動画の削除に失敗しました。');
      }
    }
  };

  if (loading || (video && (video.status === 'scanning' || video.status === 'processing'))) {
    let message = '動画を準備中...';
    if (video?.status === 'scanning') {
      message = 'セキュリティスキャンを実行中...';
    } else if (video?.status === 'processing') {
      message = video?.processing_details || '動画ファイルを準備中...';
    }
    return (
        <Container sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', mt: 4 }}>
          <CircularProgress />
          <Typography variant="h6" sx={{ mt: 2 }}>{message}</Typography>
          <Typography color="text.secondary">しばらくお待ちください...</Typography>
        </Container>
    );
  }

  if (error) return <Container><Alert severity="error">{error}</Alert></Container>;
  if (!video) return <Container><Alert severity="warning">動画が見つかりません。</Alert></Container>;
  if (video.status === 'error' || video.status === 'invalid' || video.status === 'rejected' || video.status === 'quarantined') {
    return <Container><Alert severity="error">動画の処理に失敗しました: {video.processing_details || video.status}</Alert></Container>;
  }

  const canEdit = user && (String(user.userID) === String(video.uploader_id) || user.isAdmin);

  return (
      <Container>
        <Typography variant="h4" component="h1" gutterBottom>{video.title}</Typography>

        <Box
            ref={videoContainerRef}
            sx={{ width: '800px', height: '450px', maxWidth: '100%', mx: 'auto', mb: 3, backgroundColor: 'black', position: 'relative' }}
            onMouseEnter={() => setControlsVisible(true)}
            onMouseLeave={() => setControlsVisible(false)}
        >
          <video
              ref={videoRef}
              style={{ width: '100%', height: '100%', objectFit: 'contain' }}
              onClick={togglePlayPause}
              onPlay={() => setIsPlaying(true)}
              onPause={() => setIsPlaying(false)}
              onTimeUpdate={(e) => setCurrentTime(e.currentTarget.currentTime)}
              onLoadedMetadata={(e) => setDuration(e.currentTarget.duration)}
          />
          <ControlBar sx={{ opacity: controlsVisible ? 1 : 0 }}>
            <IconButton onClick={togglePlayPause} sx={{ color: 'white' }}>
              {isPlaying ? <PauseIcon /> : <PlayArrowIcon />}
            </IconButton>
            <TimeText>{formatTime(currentTime)}</TimeText>
            <Slider
                value={currentTime}
                max={duration || 0}
                onChange={handleSeek}
                sx={{ color: 'white', mx: 2 }}
            />
            <TimeText>{formatTime(duration)}</TimeText>
            <IconButton onClick={toggleMute} sx={{ color: 'white' }}>
              {isMuted || volume === 0 ? <VolumeOffIcon /> : <VolumeUpIcon />}
            </IconButton>
            <Slider
                value={volume}
                max={1}
                step={0.1}
                onChange={handleVolumeChange}
                sx={{ width: 100, color: 'white', mx: 1 }}
            />
            {availableQualities.length > 1 && (
                <FormControl variant="standard" size="small" sx={{ minWidth: 120, ml: 1 }}>
                  <Select
                      value={currentQuality}
                      onChange={handleQualityChange}
                      sx={{ color: 'white', '& .MuiSvgIcon-root': { color: 'white' }, '&:before': { borderColor: 'white' }, '&:hover:not(.Mui-disabled):before': { borderColor: 'white' } }}
                      renderValue={(value) => (
                          <Typography sx={{ color: 'white' }}>
                            {value === 'auto' ? `自動 ${activeQualityLabel}` : availableQualities.find(q => q.level === value)?.label || ''}
                          </Typography>
                      )}
                      MenuProps={{
                        container: videoContainerRef.current,
                      }}
                  >
                    {availableQualities.map((q) => (
                        <MenuItem key={q.level} value={q.level === -1 ? 'auto' : q.level}>
                          {q.level === -1 ? `自動 ${activeQualityLabel}` : q.label}
                        </MenuItem>
                    ))}
                  </Select>
                </FormControl>
            )}
            <IconButton onClick={toggleFullscreen} sx={{ color: 'white' }}>
              {isFullscreen ? <FullscreenExitIcon /> : <FullscreenIcon />}
            </IconButton>
          </ControlBar>
        </Box>

        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mt: 2 }}>
          <Typography variant="subtitle1" color="text.secondary">
            投稿者: {uploader?.username || 'Unknown User'} | 投稿日: {new Date(video.created_at).toLocaleDateString()}
          </Typography>
          {canEdit && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                <Tooltip title="動画詳細を編集">
                  <IconButton component={RouterLink} to={`/edit-video/${video.id}`}><EditIcon /></IconButton>
                </Tooltip>
                <Button variant="contained" color="error" onClick={handleDelete}>削除</Button>
              </Box>
          )}
        </Box>
        <Typography variant="body1" sx={{ mt: 2, whiteSpace: 'pre-wrap' }}>
          {video.description}
        </Typography>
      </Container>
  );
};

export default VideoDetailPage;
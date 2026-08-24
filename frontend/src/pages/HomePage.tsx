import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Container, Typography, Grid, Card, CardContent, CardMedia, Box, CircularProgress, Alert, Avatar, Tabs, Tab, useTheme, TextField } from '@mui/material';
import { Link } from 'react-router-dom';
import VideocamIcon from '@mui/icons-material/Videocam';
import SportsEsportsIcon from '@mui/icons-material/SportsEsports';
import PublicIcon from '@mui/icons-material/Public';

interface Content {
  id: string;
  title: string;
  thumbnail_url?: string;
  uploader_id: number;
  user_id?: number;
  type: 'video' | 'game' | 'static-site';
  uploader_name?: string;
  uploader_icon?: string;
  created_at: string;
  entry_point_path?: string;
}

const HomePage = () => {
  const [allNewContents, setAllNewContents] = useState<Content[]>([]);
  const [videos, setVideos] = useState<Content[]>([]);
  const [games, setGames] = useState<Content[]>([]);
  const [staticSites, setStaticSites] = useState<Content[]>([]);
  const [loading, setLoading] = useState(true);
  const [isSearching, setIsSearching] = useState(false);
  const [error, setError] = useState('');
  const [tabIndex, setTabIndex] = useState(0);
  const [searchTerm, setSearchTerm] = useState('');
  const [debouncedSearchTerm, setDebouncedSearchTerm] = useState('');
  const theme = useTheme();
  // staticSiteDomainはHomePageでは直接使わないため削除

  useEffect(() => {
    const handler = setTimeout(() => setDebouncedSearchTerm(searchTerm), 500);
    return () => clearTimeout(handler);
  }, [searchTerm]);

  useEffect(() => {
    const fetchContents = async () => {
      try {
        setIsSearching(true);
        const query = debouncedSearchTerm ? `?q=${encodeURIComponent(debouncedSearchTerm)}` : '';

        const [videosRes, gamesRes, staticSitesRes] = await Promise.all([
          axios.get(`/api/videos${query}`),
          axios.get(`/api/games${query}`),
          axios.get(`/api/static-sites${query}`),
        ]);

        const videoData: Content[] = (videosRes.data || []).map((v: any) => ({ ...v, type: 'video', thumbnail_url: v.thumbnail_path }));
        const gameData: Content[] = (gamesRes.data || []).map((g: any) => ({ ...g, type: 'game' }));
        const staticSiteData: Content[] = (staticSitesRes.data || []).map((s: any) => ({ ...s, type: 'static-site' }));

        const combinedContents = [...videoData, ...gameData, ...staticSiteData].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime());
        
        const uploaderIds = [...new Set(combinedContents.map(c => c.uploader_id || c.user_id).filter(id => id != null && id !== 0))];

        if (uploaderIds.length > 0) {
          const profiles = await Promise.all(uploaderIds.map(id => axios.get(`/api/profile/${id}`).then(res => ({ id, ...res.data }))));
          const profileMap = new Map(profiles.map(p => [p.id, p]));

          const enrich = (content: Content) => ({
            ...content,
            uploader_name: profileMap.get(content.uploader_id || content.user_id)?.username || 'Unknown',
            uploader_icon: profileMap.get(content.uploader_id || content.user_id)?.icon_url || '',
            thumbnail_url: content.thumbnail_url || (content.type === 'static-site' ? '/default-static-site.png' : '/placeholder.png'),
          });

          setAllNewContents(combinedContents.map(enrich));
          setVideos(videoData.map(enrich));
          setGames(gameData.map(enrich));
          setStaticSites(staticSiteData.map(enrich));
        } else {
          setAllNewContents(combinedContents);
          setVideos(videoData);
          setGames(gameData);
          setStaticSites(staticSiteData);
        }
      } catch (err) {
        console.error('Failed to fetch contents:', err);
        setError('コンテンツの読み込みに失敗しました。');
      } finally {
        setLoading(false);
        setIsSearching(false);
      }
    };

    fetchContents();
  }, [debouncedSearchTerm]);

  const handleTabChange = useCallback((_event: React.SyntheticEvent, newValue: number) => setTabIndex(newValue), []);
  const handleSearchChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => setSearchTerm(e.target.value), []);

  if (loading) return <CircularProgress />;

  const renderContentGrid = (contentArray: Content[]) => (
    <Grid container spacing={3}>
      {contentArray.map((content) => {
        const isStaticSite = content.type === 'static-site';
        // HomePageのカードは常にStaticSiteDetailPageへのリンク
        const linkUrl = isStaticSite 
          ? `/static-sites/${content.id}`
          : `/${content.type}s/${content.id}`;
        const CardComponent = Link; // Linkコンポーネントを直接使用

        return (
          <Grid item key={`${content.type}-${content.id}`} xs={12} sm={6} md={4} lg={3}>
            <Card
              component={CardComponent}
              to={linkUrl}
              sx={{ height: '100%', display: 'flex', flexDirection: 'column', textDecoration: 'none', boxShadow: 1, position: 'relative', '&:hover': { transform: 'translateY(-4px)', boxShadow: 6 }, transition: 'transform 0.2s ease-in-out, box-shadow 0.2s ease-in-out' }}
            >
              <CardMedia image={content.thumbnail_url} sx={{ height: 140, backgroundSize: content.type === 'game' ? 'contain' : 'cover', bgcolor: 'black' }} />
              <CardContent sx={{ flexGrow: 1 }}>
                <Typography gutterBottom variant="h6" component="div" noWrap>{content.title}</Typography>
                <Box sx={{ display: 'flex', alignItems: 'center', mt: 1 }}>
                  <Avatar src={content.uploader_icon || '/default-icon.png'} sx={{ width: 24, height: 24, mr: 1, border: `1px solid ${theme.palette.background.paper}`, boxShadow: `0 0 0 1px ${theme.palette.grey[400]}` }} />
                  <Typography variant="body2" color="text.secondary">{content.uploader_name}</Typography>
                </Box>
              </CardContent>
              <Box sx={{ position: 'absolute', top: 8, right: 8, backgroundColor: 'rgba(100, 100, 100, 0.7)', borderRadius: 1, p: '2px 4px', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                {content.type === 'video' ? <VideocamIcon sx={{ fontSize: 18, color: 'white' }} /> : content.type === 'game' ? <SportsEsportsIcon sx={{ fontSize: 18, color: 'white' }} /> : <PublicIcon sx={{ fontSize: 18, color: 'white' }} />}
              </Box>
            </Card>
          </Grid>
        );
      })}
    </Grid>
  );

  return (
    <Container maxWidth="lg">
      <Box sx={{ my: 4 }}>
        <Typography variant="h4" component="h1" gutterBottom>コンテンツ一覧</Typography>
        <TextField label="検索" variant="outlined" fullWidth value={searchTerm} onChange={handleSearchChange} sx={{ mb: 3 }} />
        {isSearching && <Box sx={{ display: 'flex', justifyContent: 'center', my: 2 }}><CircularProgress size={24} /></Box>}
        <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
          <Tabs value={tabIndex} onChange={handleTabChange} aria-label="content category tabs">
            <Tab label="すべて" />
            <Tab label="動画" />
            <Tab label="ゲーム" />
            <Tab label="静的サイト" />
          </Tabs>
        </Box>
        {error ? <Alert severity="error">{error}</Alert> : (
          <>
            {tabIndex === 0 && renderContentGrid(allNewContents)}
            {tabIndex === 1 && renderContentGrid(videos)}
            {tabIndex === 2 && renderContentGrid(games)}
            {tabIndex === 3 && renderContentGrid(staticSites)}
          </>
        )}
      </Box>
    </Container>
  );
};

export default HomePage;
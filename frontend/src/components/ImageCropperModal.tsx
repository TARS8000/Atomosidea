import { useState, useRef } from 'react';
import { Dialog, DialogTitle, DialogContent, DialogActions, Button, Box, Typography, Slider } from '@mui/material';
import ReactCrop, { type Crop, centerCrop, makeAspectCrop } from 'react-image-crop';
import 'react-image-crop/dist/ReactCrop.css';

/** ImageCropperModal の props。 */
interface ImageCropperModalProps {
  open: boolean;
  onClose: () => void;
  // 切り抜いた画像（JPEG の Blob）を受け取る
  onSave: (croppedImage: Blob) => void;
  // 切り抜き対象の画像 URL
  imageSrc: string;
  // 切り抜き範囲の縦横比（1 のときは円形の枠で表示する）
  aspect: number;
}

/**
 * 画像の切り抜き範囲を指定するダイアログ。
 * 保存時に選択範囲を元画像の解像度で canvas に描画し、JPEG の Blob として onSave に渡す。
 */
const ImageCropperModal = ({ open, onClose, onSave, imageSrc, aspect }: ImageCropperModalProps) => {
  const imgRef = useRef<HTMLImageElement>(null);
  // 操作中の切り抜き範囲（% 単位）
  const [crop, setCrop] = useState<Crop>();
  // 確定した切り抜き範囲（表示サイズ基準の px 単位）。保存時に使う
  const [completedCrop, setCompletedCrop] = useState<Crop>();
  // プレビュー表示用の拡大率と回転角度（保存結果には反映されない）
  const [scale, setScale] = useState(1);
  const [rotate, setRotate] = useState(0);

  // 画像の読み込み時に、幅 90%・指定の縦横比の切り抜き範囲を中央に配置する。
  function onImageLoad(e: React.SyntheticEvent<HTMLImageElement>) {
    const { width, height } = e.currentTarget;
    const newCrop = centerCrop(
      makeAspectCrop(
        {
          unit: '%',
          width: 90,
        },
        aspect,
        width,
        height
      ),
      width,
      height
    );
    setCrop(newCrop);
  }

  // 選択範囲を JPEG（品質 0.95）の Blob に変換して onSave に渡す。
  const handleSave = async () => {
    if (!completedCrop || !imgRef.current) {
      return;
    }

    // 1. 表示サイズと元画像サイズの比率を求め、出力する canvas の大きさを決める
    const canvas = document.createElement('canvas');
    const image = imgRef.current;
    const scaleX = image.naturalWidth / image.width;
    const scaleY = image.naturalHeight / image.height;
    
    canvas.width = completedCrop.width * scaleX;
    canvas.height = completedCrop.height * scaleY;
    
    const ctx = canvas.getContext('2d');
    if (!ctx) {
      return;
    }

    // 2. 元画像から選択範囲を切り出して canvas 全体に描画する（拡大率・回転角度は使っていない）
    ctx.drawImage(
      image,
      completedCrop.x * scaleX,
      completedCrop.y * scaleY,
      completedCrop.width * scaleX,
      completedCrop.height * scaleY,
      0,
      0,
      canvas.width,
      canvas.height
    );

    // 3. JPEG に変換して呼び出し元へ渡す
    canvas.toBlob((blob) => {
      if (blob) {
        onSave(blob);
      }
    }, 'image/jpeg', 0.95);
  };

  // 選択枠の角丸を指定するための CSS 変数。ただし react-image-crop の CSS はこの変数名を参照していないため、
  // 実際の円形表示は circularCrop によるもの。
  const cropStyle: React.CSSProperties = {
    '--ReactCrop__crop-selection': `
      border-radius: ${aspect === 1 ? '50%' : '6px'}
    `,
  } as React.CSSProperties;

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>画像を調整</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'flex', justifyContent: 'center', my: 2, maxHeight: '60vh' }}>
          <ReactCrop
            crop={crop}
            onChange={(_, percentCrop) => setCrop(percentCrop)}
            onComplete={(c) => setCompletedCrop(c)}
            aspect={aspect}
            circularCrop={aspect === 1}
            style={cropStyle}
          >
            <img
              ref={imgRef}
              alt="Crop me"
              src={imageSrc}
              style={{ transform: `scale(${scale}) rotate(${rotate}deg)`, maxHeight: '60vh' }}
              onLoad={onImageLoad}
            />
          </ReactCrop>
        </Box>

        {/* プレビューの拡大率・回転角度を調整するスライダー */}
        <Box sx={{ display: 'flex', gap: 4, alignItems: 'center', mt: 2 }}>
          <Typography sx={{ minWidth: '50px' }}>拡大:</Typography>
          <Slider
            value={scale}
            min={0.5}
            max={4}
            step={0.1}
            aria-labelledby="scale-slider"
            onChange={(_, newValue) => setScale(newValue as number)}
          />
          <Typography sx={{ minWidth: '50px' }}>回転:</Typography>
          <Slider
            value={rotate}
            min={-180}
            max={180}
            step={1}
            aria-labelledby="rotate-slider"
            onChange={(_, newValue) => setRotate(newValue as number)}
          />
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>キャンセル</Button>
        <Button variant="contained" onClick={handleSave}>保存</Button>
      </DialogActions>
    </Dialog>
  );
};

export default ImageCropperModal;
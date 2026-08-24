import { useState, useRef } from 'react';
import { Dialog, DialogTitle, DialogContent, DialogActions, Button, Box, Typography, Slider } from '@mui/material';
import ReactCrop, { type Crop, centerCrop, makeAspectCrop } from 'react-image-crop';
import 'react-image-crop/dist/ReactCrop.css';

interface ImageCropperModalProps {
  open: boolean;
  onClose: () => void;
  onSave: (croppedImage: Blob) => void;
  imageSrc: string;
  aspect: number;
}

const ImageCropperModal = ({ open, onClose, onSave, imageSrc, aspect }: ImageCropperModalProps) => {
  const imgRef = useRef<HTMLImageElement>(null);
  const [crop, setCrop] = useState<Crop>();
  const [completedCrop, setCompletedCrop] = useState<Crop>();
  const [scale, setScale] = useState(1);
  const [rotate, setRotate] = useState(0);

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

  const handleSave = async () => {
    if (!completedCrop || !imgRef.current) {
      return;
    }

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

    canvas.toBlob((blob) => {
      if (blob) {
        onSave(blob);
      }
    }, 'image/jpeg', 0.95);
  };

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
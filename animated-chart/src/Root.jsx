import React from 'react';
import { Composition } from 'remotion';
import { FiveBarChart } from './FiveBarChart';

export const RemotionRoot = () => (
  <Composition
    id="FiveBarChart"
    component={FiveBarChart}
    durationInFrames={150}
    fps={30}
    width={1920}
    height={1080}
  />
);

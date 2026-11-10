import { createRoot } from 'react-dom/client';
import '@fontsource-variable/geist';
import '@fontsource-variable/jetbrains-mono';
import './style.css';
import { App } from './App';

createRoot(document.getElementById('root')!).render(<App/>);

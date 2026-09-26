import React from 'react';
import {createRoot} from 'react-dom/client';
import App from './App';
import './styles.css';
import './admin.css';
import './editor.css';
import './frontend.css';
import './classroom.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);

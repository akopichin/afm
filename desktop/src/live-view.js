import { WebContentsView } from 'electron';
import { randomUUID } from 'node:crypto';
import { shortcutAction } from './shortcuts.js';

// The tab owns a private Chromium session. Hiding the tab preserves the agent's page.
export class LiveView {
  constructor(window, file, shortcut) {
    this.window=window;
    this.view=new WebContentsView({webPreferences:{
      sandbox:true,nodeIntegration:false,contextIsolation:true,partition:'afm-stage-'+randomUUID()
    }});
    const wc=this.view.webContents;
    wc.setWindowOpenHandler(()=>({action:'deny'}));
    wc.on('will-navigate',event=>event.preventDefault());
    wc.session.setPermissionRequestHandler((_wc,_permission,reply)=>reply(false));
    wc.on('before-input-event',(event,input)=>{
      const action=shortcutAction(input);
      if (!action) return;
      event.preventDefault();
      if (typeof action !== 'number') window.webContents.focus();
      shortcut(action);
    });
    this.ready=wc.loadFile(file);
    this.view.setVisible(false);
    window.contentView.addChildView(this.view);
  }
  place(bounds) {
    if(!bounds){this.view.setVisible(false);return}
    const [width,height]=this.window.getContentSize();
    const x=Math.max(0,Math.min(width,bounds.x));
    const y=Math.max(0,Math.min(height,bounds.y));
    this.view.setBounds({x,y,width:Math.min(width-x,bounds.width),height:Math.min(height-y,bounds.height)});
    this.view.setVisible(true);
  }
  close(){
    this.window.contentView.removeChildView(this.view);
    this.view.webContents.close();
  }
}

(() => {
  'use strict';
  const stage = document.querySelector('#stage');
  const motion = document.querySelector('#motion');
  const name = document.querySelector('#view-name');
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)');
  const views = ['Assembled', 'Dependency graph', 'Event relay', 'Artifact gate', 'Specialist roles', 'Persistent state', 'Versioned assembly'];
  let active = 0, progress = 0, paused = reduced.matches, journey = 0;
  let raf = 0, last = 0, clock = 0, render = null;
  let mx = 0, my = 0, sx = 0, sy = 0;
  let heldPhase = 0, heldJourney = 0;

  function frame(now) {
    raf = 0;
    if (!render || document.hidden) { last = 0; return; }
    const dt = last ? Math.min((now - last) / 1000, 0.06) : 0;
    last = now;
    if (!paused) clock += dt;
    const ease = 1 - Math.exp(-dt * 5);
    if (!paused) {
      sx += (mx - sx) * ease;
      sy += (my - sy) * ease;
    }
    render(clock, paused ? heldPhase : progress, sx, sy);
    if (!paused) schedule();
  }
  function schedule() { if (render && !raf && !document.hidden) raf = requestAnimationFrame(frame); }
  function updateChapter() { schedule(); }
  function switchMotion(stop) {
    if (stop && !paused) { heldPhase = progress; heldJourney = journey; }
    paused = stop; last = 0;
    document.documentElement.classList.toggle('maps-moving', !paused);
    motion.textContent = paused ? 'Play motion' : 'Pause motion';
    schedule();
  }
  motion.addEventListener('click', () => switchMotion(!paused));
  reduced.addEventListener('change', e => switchMotion(e.matches));
  addEventListener('scroll', updateChapter, {passive:true});
  addEventListener('resize', updateChapter);
  document.addEventListener('visibilitychange', schedule);
  window.addEventListener('pointermove', e => {
    if (e.pointerType !== 'mouse') return;
    mx = (e.clientX / innerWidth - 0.5) * 2;
    my = (e.clientY / innerHeight - 0.5) * 2;
  });
  document.documentElement.addEventListener('pointerleave', () => {mx = my = 0;});
  updateChapter();
  switchMotion(paused);
  document.fonts.ready.then(updateChapter);

  const T = window.AFM3;
  if (!T) return;
  let renderer;
  try {
    renderer = new T.WebGLRenderer({canvas:stage.querySelector('canvas'), alpha:true, antialias:true, powerPreference:'high-performance'});
  } catch { return; }
  renderer.setClearColor(0x101626,0);
  renderer.outputColorSpace = T.SRGBColorSpace;
  renderer.toneMapping = T.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.02;

  const scene = new T.Scene();
  const field = createSignalField(T, scene);
  const camera = new T.PerspectiveCamera(29, 1, .1, 60);
  const studio = new T.RoomEnvironment();
  const gen = new T.PMREMGenerator(renderer);
  const reflections = gen.fromScene(studio, .02);
  scene.environment = reflections.texture;
  gen.dispose(); studio.dispose();

  function lamp(x,y,z,color,intensity) {
    const light = new T.DirectionalLight(color,intensity);
    light.position.set(x,y,z); scene.add(light);
  }
  lamp(-3,6,7,0xe7ecff,2.35);
  lamp(4,-2,-1,0x6476fb,3.0);
  lamp(-6,-1,0,0xb2bffb,1.6);
  const metal = (color, roughness, metalness=1) => new T.MeshStandardMaterial({color,metalness,roughness});
  const black = metal(0x242a3b,.39,.9);
  const dark = metal(0x465475,.29);
  const titanium = metal(0xc6cdde,.23);
  const steel = metal(0x858fac,.26);
  const violet = metal(0x788afa,.24,.85);
  const ceramic = metal(0xabb9ff,.24,.58);
  const light = new T.MeshStandardMaterial({color:0xbec9ff,emissive:0x778fff,emissiveIntensity:.62,metalness:.43,roughness:.22});

  const assembly = new T.Group(); scene.add(assembly);
  const layers = [];
  function layer(z) {
    const group = new T.Group();
    assembly.add(group); layers.push({group,z}); group.position.z = z;
    return group;
  }
  const back = layer(-.38), casework = layer(-.10), face = layer(.24), front = layer(.39);
  const tau = Math.PI * 2;
  const vertices = (radius,n=6) => Array.from({length:n},(_,i)=>new T.Vector2(Math.sin(i*tau/n)*radius,Math.cos(i*tau/n)*radius));
  function roundLoop(target,points,round) {
    points.forEach((v,i) => {
      const a = v.clone().lerp(points[(i+points.length-1)%points.length],round);
      const b = v.clone().lerp(points[(i+1)%points.length],round);
      if (!i) target.moveTo(a.x,a.y); else target.lineTo(a.x,a.y);
      target.quadraticCurveTo(v.x,v.y,b.x,b.y);
    });target.closePath();
  }
  function ring(parent,outer,inner,depth,z,mat,bevel=.014) {
    const shape = new T.Shape(), hole = new T.Path();
    roundLoop(shape,vertices(outer),.047); roundLoop(hole,vertices(inner).reverse(),.047);
    shape.holes.push(hole);
    const geometry = new T.ExtrudeGeometry(shape,{depth,bevelEnabled:bevel>0,bevelThickness:bevel,bevelSize:bevel,bevelSegments:3,curveSegments:5});
    const mesh = new T.Mesh(geometry,mat); mesh.position.z=z;parent.add(mesh);return mesh;
  }
  ring(back,2.34,1.76,.13,-.1,black,.047);
  ring(back,2.39,2.20,.028,-.12,titanium);
  ring(casework,2.38,1.77,.22,-.12,dark,.05);
  for(let i=0;i<6;i++) ring(casework,2.41,2.31,.014,-.11+i*.053,i%2?steel:titanium,.006);
  ring(face,2.37,1.82,.095,-.10,black,.027);
  ring(face,2.365,2.30,.035,.03,titanium);
  ring(face,1.89,1.80,.055,.016,steel);
  ring(front,2.34,2.27,.045,-.02,violet);
  ring(front,1.90,1.805,.052,-.02,ceramic);
  ring(front,2.236,2.215,.012,-.018,steel,.004);

  const scratch = new T.Object3D();
  function instances(parent,geometry,material,poses) {
    const mesh = new T.InstancedMesh(geometry,material,poses.length);
    poses.forEach(([x,y,z,rz=0,rx=0,ry=0,sw=1,sh=1,sd=1],i) => {
      scratch.position.set(x,y,z);scratch.rotation.set(rx,ry,rz,'ZXY');scratch.scale.set(sw,sh,sd);
      scratch.updateMatrix();mesh.setMatrixAt(i,scratch.matrix);
    });
    parent.add(mesh);
  }
  const cylinder=(r,d,sides=12)=>new T.CylinderGeometry(r,r,d,sides).rotateX(Math.PI/2);
  const drives = [], signals = [];
  const gearTeeth = [];
  for(let j=0;j<24;j++){
    const angle=j*tau/24;
    gearTeeth.push([Math.sin(angle)*.122,Math.cos(angle)*.122,0,-angle]);
  }
  for(let i=0;i<6;i++){
    const a=i*tau/6;
    const drive=new T.Group();
    drive.position.set(Math.sin(a)*2.105,Math.cos(a)*2.105,.14);
    face.add(drive);drives.push(drive);
    drive.add(new T.Mesh(cylinder(.119,.043,48),dark));
    instances(drive,new T.BoxGeometry(.018,.028,.041),steel,gearTeeth);
    const bezel=new T.Mesh(new T.TorusGeometry(.088,.012,8,40),titanium);
    bezel.position.z=.028;drive.add(bezel);
    const hub=new T.Mesh(cylinder(.046,.030,6),violet);
    hub.position.z=.032;drive.add(hub);
    const perforations=[];
    for(let j=0;j<6;j++){
      const t=j*tau/6;perforations.push([Math.sin(t)*.068,Math.cos(t)*.068,.024]);
    }
    instances(drive,cylinder(.009,.009,8),black,perforations);
  }
  const banks=[];
  for(let s=0;s<6;s++){
    const bank=new T.Group();face.add(bank);banks.push(bank);
    const theta=(s+.5)*tau/6, nx=Math.sin(theta),ny=Math.cos(theta);
    const tx=Math.cos(theta),ty=-Math.sin(theta);
    const pose=(t,r,z,extra=0)=>[nx*r+tx*t,ny*r+ty*t,z,-theta+extra];
    const ribs=[],edges=[],bolts=[],sockets=[],ticks=[];
    for(let k=0;k<32;k++){
      const t=(k-15.5)*.0585, r=1.81;
      ribs.push(pose(t,r,.036,-.14));
      edges.push(pose(t-.012,r,.104,-.14));
    }
    for(const end of [-1,1]){
      bolts.push(pose(end*.918,1.735,.122));sockets.push(pose(end*.918,1.735,.144));
    }
    for(let i=0;i<9;i++)ticks.push(pose((i-4)*.065,2.005,.055));
    instances(bank,new T.BoxGeometry(.038,.285,.135),s%3===0?violet:steel,ribs);
    instances(bank,new T.BoxGeometry(.008,.285,.012),titanium,edges);
    instances(bank,cylinder(.040,.05,20),titanium,bolts);
    instances(bank,cylinder(.015,.006,6),black,sockets);
    instances(bank,new T.BoxGeometry(.021,.021,.01),light,ticks);
  }

  const sample=vertices(2.272);
  for(let i=0;i<6;i++){
    const packet = new T.Mesh(new T.BoxGeometry(.13,.023,.026),light);
    front.add(packet);signals.push(packet);
  }

  const core = new T.Group();assembly.add(core);
  const ball = new T.Mesh(new T.SphereGeometry(.525,64,48),new T.MeshPhysicalMaterial({color:0x7388ff,roughness:.32,metalness:.70,clearcoat:.52,clearcoatRoughness:.22}));
  core.add(ball);
  function torus(parent,r,width,mat){
    const mesh=new T.Mesh(new T.TorusGeometry(r,width,10,128),mat);parent.add(mesh);return mesh;
  }
  const innerRotor=new T.Group();core.add(innerRotor);
  innerRotor.rotation.set(.70,.45,-.26);
  for(const z of [-.12,-.085,.085,.12]){
    const seam=torus(innerRotor,Math.sqrt(.525*.525-z*z),.007,z<0?dark:steel);
    seam.position.z=z;
  }
  const gyros=[];
  for(let i=0;i<3;i++){const rotor=new T.Group();core.add(rotor);gyros.push(rotor);
    torus(rotor,.574+i*.066,i===2?.013:.023,i===1?violet:titanium);
  }
  const dial=new T.Group();core.add(dial);
  const indexes=[];
  for(let i=0;i<72;i++){
    const a=tau*i/72;indexes.push([Math.sin(a)*.774,Math.cos(a)*.774,0,-a,0,0,1,(i%6===0?2:1),1]);
  }
  instances(dial,new T.BoxGeometry(.009,.027,.013),ceramic,indexes);
  const rails=new T.Group();assembly.add(rails);
  const spokes=[];
  for(let i=0;i<6;i++) {
    const theta=tau*i/6;
    for(const offset of [-.04,.04]){
      const x=Math.sin(theta)*1.28+Math.cos(theta)*offset;
      const y=Math.cos(theta)*1.28-Math.sin(theta)*offset;
      spokes.push([x,y,-.27,-theta]);
    }
  }
  instances(rails,new T.BoxGeometry(.013,1.00,.023),dark,spokes);

  // Three independent instrument dials orbit the six-sided chassis.
  const carriage = new T.Group();scene.add(carriage);
  const orbits = [];
  for(let i=0;i<3;i++){
    const rotor=new T.Group();carriage.add(rotor);orbits.push(rotor);
    const radius=2.70+i*.29;
    const segments=3+i;
    for(let j=0;j<segments;j++){
      const arc=new T.Mesh(new T.TorusGeometry(radius,.012+i*.003,8,68,tau/segments*(.65-.07*i)),i===1?dark:steel);
      arc.rotation.z=j*tau/segments;rotor.add(arc);
      const emitter=new T.Mesh(new T.TorusGeometry(radius,.021,8,16,.07+i*.016),i===1?ceramic:light);
      emitter.rotation.z=arc.rotation.z+.10;rotor.add(emitter);
    }
    const ticks=[];
    for(let j=0;j<100;j++){
      if(j%25>17)continue;
      const a=j*tau/100;
      ticks.push([Math.sin(a)*radius,Math.cos(a)*radius,0,-a,0,0,1,j%5===0?2.6:1,1]);
    }
    instances(rotor,new T.BoxGeometry(.015,.037,.016),i===1?steel:violet,ticks);
  }
  // Hinged vanes expose the spindle as the run reaches a review gate.
  const irises=[];
  const bladeShape=new T.Shape();
  bladeShape.moveTo(-.09,0);bladeShape.lineTo(.10,.05);bladeShape.lineTo(.19,.53);
  bladeShape.lineTo(.10,.68);bladeShape.lineTo(-.045,.49);bladeShape.closePath();
  const bladeGeometry=new T.ExtrudeGeometry(bladeShape,{depth:.047,bevelEnabled:true,bevelSize:.014,bevelThickness:.015,bevelSegments:2});
  for(let j=0;j<6;j++){
    const a=tau*j/6;
    const hinge=new T.Group();
    hinge.position.set(Math.sin(a)*1.54,Math.cos(a)*1.54,-.21);
    hinge.rotation.z=Math.PI-a;assembly.add(hinge);
    const blade=new T.Mesh(bladeGeometry,j%2?titanium:violet);hinge.add(blade);
    const inset=new T.Mesh(new T.BoxGeometry(.026,.29,.013),light);
    inset.position.set(.045,.29,.063);blade.add(inset);irises.push(blade);
  }
  let anchorX=0,anchorY=0;
  function resize() {
    const w = stage.clientWidth,h = stage.clientHeight;
    if (!w || !h) return;
    renderer.setPixelRatio(Math.min(devicePixelRatio||1,1.5, 2000 / Math.max(w,h)));
    renderer.setSize(w,h,false);
    camera.aspect=w/h;
    const vertical=Math.max(6.8,6.8/camera.aspect);
    const focus=new T.Vector2(.5,.5);
    anchorX=(focus.x-.5)*vertical*camera.aspect;
    anchorY=(focus.y-.5)*vertical;
    field.resize(camera.aspect,focus);
    camera.position.z=vertical/(2*Math.tan(T.MathUtils.degToRad(camera.fov/2)));
    camera.updateProjectionMatrix();schedule();
  }
  const poses=[
    [.30,.18,-.51,-.045],[.70,.35,-.36,.13],[.45,-.12,.28,-.13],[.92,.55,-.28,.04],
    [1.2,.30,.36,-.11],[.55,-.35,-.22,.12],[.30,.18,-.51,-.045]
  ];
  render=(t,p,x,y)=>{
    const index=Math.min(5,Math.floor(p)), k=T.MathUtils.smoothstep(p-index,0,1);
    const v=poses[index].map((a,i)=>T.MathUtils.lerp(a,poses[index+1][i],k));
    const [spread,tilt,yaw,roll]=v;
    const breathe=.5-.5*Math.cos(t*.42);
    const drift=paused?heldJourney:journey;
    field.frame(t,drift,x,y,anchorX,anchorY);
    assembly.rotation.set(.08+tilt+.16*y+.23*Math.sin(t*.19),yaw+.29*x+.76*Math.sin(t*.17),roll+.056*t+.07*Math.sin(p*1.9));
    assembly.position.set(anchorX+.14*x+.10*Math.sin(drift*.7),anchorY-.09*y+.12*Math.sin(drift*1.3),0);
    layers.forEach(({group,z},i)=>{
      group.position.z=z*(1.22+spread*2.7+breathe*.95);
      group.rotation.z=(i-1.5)*(.065*Math.sin(t*.35)+spread*.052);
    });
    carriage.position.copy(assembly.position);
    orbits.forEach((rotor,i)=>{
      rotor.position.z=-.34+i*.17;
      rotor.rotation.set(.28+i*.28+.20*Math.sin(t*.13+i)+.09*y,
        -.38+i*.32+.28*Math.sin(t*.12+i+p*.4)+.12*x,(i%2?-1:1)*t*(.13+i*.035)+i+.19*p);
    });
    irises.forEach((blade,i)=>{
      blade.rotation.z=.24+.50*breathe+.16*Math.sin(t*.48+i*1.05)+spread*.25;
      blade.rotation.x=.25+spread*.42+breathe*.32;
    });
    banks.forEach((b,i)=>{
      const angle=(i+.5)*tau/6;
      const loosen=.045+spread*.17*(1+Math.sin(i*1.9+t*.35)*.55)+breathe*.12;
      b.position.set(Math.sin(angle)*loosen,Math.cos(angle)*loosen,spread*.10+breathe*.12);
    });
    gyros.forEach((r,i)=>r.rotation.set(.52+i*.5+Math.sin(t*.24+i)*.28,.42+i*.61+t*(i===1?.22:-.12),t*.17+i));
    drives.forEach((drive,i)=>{drive.rotation.z=(i%2?1:-1)*(t*.72+spread*.5);});
    signals.forEach((packet,i)=>{
      const travel=(t*.16+i/6+p*.12)%1;
      const n=travel*6, ix=Math.floor(n);
      const a=sample[ix], b=sample[(ix+1)%6];
      packet.position.set(T.MathUtils.lerp(a.x,b.x,n-ix),T.MathUtils.lerp(a.y,b.y,n-ix),.045);
      packet.rotation.z=Math.atan2(b.y-a.y,b.x-a.x);
    });
    innerRotor.rotation.y=.45+t*.16;
    core.position.z=.16+spread*.30;dial.rotation.z=-t*.15;dial.rotation.x=.20+Math.sin(t*.12)*.1;
    renderer.render(scene,camera);
  };
  stage.querySelector('canvas').addEventListener('webglcontextlost',e=>{
    e.preventDefault();render=null;cancelAnimationFrame(raf);stage.classList.remove('ready');motion.hidden=true;
  });
  new ResizeObserver(resize).observe(stage);resize();
  render(0,progress,0,0);stage.classList.add('ready');motion.hidden=false;switchMotion(paused);
})();
